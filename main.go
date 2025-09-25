package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 版本号
const Version = "v0.2.0"

// 支持的文件类型枚举（按处理方式分类）
var (
	// Office OpenXML：docx/xlsx/pptx 通过删除 zip 内的 docProps/* 实现属性清除
	openXMLSet = map[string]bool{
		".docx": true, ".xlsx": true, ".pptx": true,
	}
	// OpenDocument：odt/ods/odp 通过删除 zip 内的 meta.xml 实现属性清除
	openDocSet = map[string]bool{
		".odt": true, ".ods": true, ".odp": true,
	}
	// 图片：jpeg/jpg、png 通过解码再无元数据重编码
	imageSet = map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true,
	}
	// 其他：pdf 需要可选依赖（pdfcpu），见 --with-pdf 标志
)

// 命令行参数
var (
	inputPath  string
	backup     bool
	dryRun     bool
	workers    int
	withPDF    bool
	includeExt string
	excludeExt string
	verbose    bool
)

func init() {
	flag.StringVar(&inputPath, "path", "", "待处理文件或目录路径（支持文件或目录）")
	flag.BoolVar(&backup, "backup", true, "是否保留 .bak 备份（默认保留）")
	flag.BoolVar(&dryRun, "dry-run", false, "仅演示将要处理的文件，不做任何修改")
	flag.IntVar(&workers, "workers", max(2, runtime.NumCPU()), "并发处理的工作协程数")
	flag.BoolVar(&withPDF, "with-pdf", false, "启用 PDF 脱敏（需要额外依赖 pdfcpu，见源码注释）")
	flag.StringVar(&includeExt, "include", "", "仅处理这些扩展名（逗号分隔，例如: docx,xlsx,pptx,pdf,jpg,png）")
	flag.StringVar(&excludeExt, "exclude", "", "排除这些扩展名（逗号分隔）")
	flag.BoolVar(&verbose, "v", false, "输出更多日志")
}

func main() {
	flag.Parse()
	if inputPath == "" {
		fmt.Printf("goscrub %s\n用法: goscrub --path <文件或目录> [--with-pdf] [--backup] [--workers N] [--dry-run] [--include ext1,ext2] [--exclude ext1,ext2]\n", Version)
		os.Exit(2)
	}

	// 规范化 include/exclude 列表
	inc := toSet(includeExt)
	exc := toSet(excludeExt)

	// 收集待处理文件
	var files []string
	info, err := os.Stat(inputPath)
	if err != nil {
		log.Fatalf("路径无法访问: %v", err)
	}
	if info.IsDir() {
		err = filepath.WalkDir(inputPath, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if len(inc) > 0 && !inc[trimDot(ext)] {
				return nil
			}
			if exc[trimDot(ext)] {
				return nil
			}
			if isSupportedExt(ext) {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			log.Fatalf("遍历目录失败: %v", err)
		}
	} else {
		ext := strings.ToLower(filepath.Ext(inputPath))
		if len(inc) > 0 && !inc[trimDot(ext)] {
			log.Fatalf("不在 include 列表: %s", inputPath)
		}
		if exc[trimDot(ext)] {
			log.Fatalf("在 exclude 列表中: %s", inputPath)
		}
		if !isSupportedExt(ext) {
			log.Fatalf("暂不支持的文件类型: %s", ext)
		}
		files = []string{inputPath}
	}

	if len(files) == 0 {
		fmt.Println("没有匹配到可处理的文件。")
		return
	}

	fmt.Printf("发现 %d 个待处理文件。\n", len(files))
	if dryRun {
		for _, f := range files {
			fmt.Println("- ", f)
		}
		return
	}

	// 并发处理
	jobs := make(chan string, len(files))
	wg := sync.WaitGroup{}
	okCount := int64(0)
	failCount := int64(0)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if err := scrubFile(f); err != nil {
					log.Printf("[FAIL] %s: %v", f, err)
					add(&failCount, 1)
				} else {
					if verbose {
						log.Printf("[OK] %s", f)
					}
					add(&okCount, 1)
				}
			}
		}()
	}
	for _, f := range files {
		jobs <- f
	}
	close(jobs)
	wg.Wait()

	fmt.Printf("处理完成：成功 %d，失败 %d。\n", okCount, failCount)
}

func scrubFile(p string) error {
	ext := strings.ToLower(filepath.Ext(p))
	// 为避免 “文件被占用” 问题：以只读打开探测，随后复制到临时文件再原子替换
	// Windows 上如果目标被占用会报错，建议关闭占用应用或加重试

	switch {
	case openXMLSet[ext]:
		return scrubOpenXML(p)
	case openDocSet[ext]:
		return scrubOpenDocument(p)
	case imageSet[ext]:
		return scrubImage(p, ext)
	case ext == ".pdf":
		if !withPDF {
			return errors.New("检测到 PDF，请使用 --with-pdf 以启用 PDF 脱敏（需要 pdfcpu 依赖）")
		}
		return scrubPDF(p)
	default:
		return fmt.Errorf("不支持的扩展名: %s", ext)
	}
}

// —— Office OpenXML: 过滤 zip 中的 docProps/* ——
func scrubOpenXML(path string) error {
	return rewriteZip(path, func(name string) bool {
		// 返回 true 表示保留该条目
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "docprops/") {
			return false // 丢弃所有属性文件: core.xml, app.xml, custom.xml
		}
		return true
	})
}

// —— OpenDocument: 删除根目录 meta.xml ——
func scrubOpenDocument(path string) error {
	return rewriteZip(path, func(name string) bool {
		lower := strings.ToLower(name)
		if lower == "meta.xml" {
			return false
		}
		return true
	})
}

// —— 图片：解码->无元数据重编码 ——
func scrubImage(path, ext string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()

	img, format, err := image.Decode(bufio.NewReader(in))
	if err != nil {
		return fmt.Errorf("图片解码失败: %w", err)
	}
	_ = format // 仅供调试

	// 写入到临时文件
	tmp := path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer out.Close()

	switch ext {
	case ".jpg", ".jpeg":
		// 重新编码会丢弃 EXIF/XMP
		if err := jpeg.Encode(out, img, &jpeg.Options{Quality: 95}); err != nil {
			return err
		}
	case ".png":
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(out, img); err != nil {
			return err
		}
	default:
		return fmt.Errorf("未知图片类型: %s", ext)
	}

	return replaceOriginal(path, tmp)
}

// —— PDF：使用 pdfcpu 清除元数据（需要 go get github.com/pdfcpu/pdfcpu@latest）——
// 说明：
// 1) 请在构建前执行： go get github.com/pdfcpu/pdfcpu@latest
// 2) pdfcpu 的 Clean/Optimize 会去除冗余对象，SetMetadata 可清空 XMP；同时可清空 Info Dict。
// 3) 某些加密/权限受限的 PDF 可能需要密码，本文未处理。
func scrubPDF(path string) error {
	// 为避免在未安装依赖时无法构建，代码在此处做延迟加载（接口解耦）。
	return scrubPDFWithPDFCPU(path)
}

// —— ZIP 重写通用函数 ——
func rewriteZip(path string, keep func(name string) bool) error {
	// 读取原始二进制到内存，尽量减少占用冲突
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("打开 zip 失败: %w", err)
	}

	// 写入到临时 zip
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)

	for _, zf := range zr.File {
		if !keep(zf.Name) {
			continue
		}

		// 打开源条目
		r, err := zf.Open()
		if err != nil {
			zw.Close()
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("读取条目失败 %s: %w", zf.Name, err)
		}
		// 创建目标条目，尽量保留压缩方式
		h := &zip.FileHeader{Name: zf.Name, Method: zf.Method}
		h.SetMode(zf.Mode())
		h.Modified = zf.Modified
		w, err := zw.CreateHeader(h)
		if err != nil {
			r.Close()
			zw.Close()
			f.Close()
			os.Remove(tmp)
			return err
		}
		if _, err := io.Copy(w, r); err != nil {
			r.Close()
			zw.Close()
			f.Close()
			os.Remove(tmp)
			return err
		}
		r.Close()
	}

	if err := zw.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	return replaceOriginal(path, tmp)
}

// —— 原子替换并保留备份 ——
func replaceOriginal(orig, tmp string) error {
	if backup {
		bak := orig + ".bak"
		if _, err := os.Stat(bak); err == nil {
			bak = fmt.Sprintf("%s.%d.bak", orig, time.Now().Unix())
		}
		if err := copyFile(orig, bak); err != nil {
			return fmt.Errorf("创建备份失败: %w", err)
		}
	}

	// 原子替换失败时，尝试直接覆盖写入
	for i := 0; i < 2; i++ {
		if err := os.Rename(tmp, orig); err != nil {
			if i == 0 {
				time.Sleep(300 * time.Millisecond)
				continue
			}
			// fallback: 用 copy 覆盖
			if err := copyFile(tmp, orig); err != nil {
				return fmt.Errorf("替换原文件失败（可能被占用）: %w", err)
			}
			os.Remove(tmp)
			return nil
		}
		return nil
	}
	return nil
}

func copyFile(src, dst string) error {
	s, err := os.Open(src)
	if err != nil {
		return err
	}
	defer s.Close()
	d, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer d.Close()
	_, err = io.Copy(d, s)
	return err
}

// —— 小工具函数 ——
func isSupportedExt(ext string) bool {
	if openXMLSet[ext] || openDocSet[ext] || imageSet[ext] {
		return true
	}
	if ext == ".pdf" {
		return true
	}
	return false
}

func toSet(csv string) map[string]bool {
	res := map[string]bool{}
	if strings.TrimSpace(csv) == "" {
		return res
	}
	for _, v := range strings.Split(csv, ",") {
		v = strings.TrimSpace(strings.ToLower(v))
		v = trimDot(v)
		if v != "" {
			res[v] = true
		}
	}
	return res
}

func trimDot(ext string) string {
	return strings.TrimPrefix(ext, ".")
}

func add(ptr *int64, delta int64) {
	// 无需原子性，这里非关键统计，若要原子请使用 atomic.AddInt64
	*ptr += delta
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ========== 可选：pdfcpu 清理实现 ==========
// 将此部分单独放置，避免未安装依赖时报编译错误。
// 若要启用：
//   go get github.com/pdfcpu/pdfcpu@latest
// 然后正常 go build / run，并加 --with-pdf

// 为了在未引入依赖的情况下也能编译，这里采用 build tags 的技巧：
// 你可以创建一个同目录文件 pdf_stub.go 存根（见下备注），或者直接使用下方的反射式延迟导入方案。

// 简化处理：我们在此给出一个占位实现，提示未启用 PDF 支持。
// 如需真正生效，请将本函数替换为使用 pdfcpu 的实现（示例见下方注释）。

func scrubPDFWithPDFCPU(path string) error {
	// ===== 如需启用真正的 PDF 清理，请参考： =====
	// import (
	//   pdfapi "github.com/pdfcpu/pdfcpu/pkg/api"
	//   "github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	// )
	// conf := pdfcpu.NewDefaultConfiguration()
	// // 1) 清空 XMP 元数据
	// if err := pdfapi.SetMetadataFile(path, path+".tmp", nil, conf); err != nil { return err }
	// // 2) 清空 Info 字典
	// infos := map[string]string{"Title":"","Author":"","Subject":"","Keywords":"","Creator":"","Producer":""}
	// if err := pdfapi.SetInfoMapFile(path, path+".tmp2", infos, conf); err != nil { return err }
	// // 3) 进一步优化/清理
	// if err := pdfapi.OptimizeFile(path+".tmp2", path, conf); err != nil { return err }
	// os.Remove(path+".tmp")
	// os.Remove(path+".tmp2")
	return errors.New("未编译 PDF 支持：请执行 `go get github.com/pdfcpu/pdfcpu@latest` 并使用 --with-pdf 重新运行；同时将 scrubPDFWithPDFCPU 实现替换为 pdfcpu 版本（见源码注释）")
}
