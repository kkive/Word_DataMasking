

---

# Word_DataMasking 用户手册

## 简介

`Word_DataMasking` 是一个使用 **Go 语言**编写的文档属性脱敏工具，支持递归处理目录或单个文件。
它可以删除常见文档的 **详情信息/元数据**（作者、创建时间、修改人、应用信息、图片 EXIF 等），以降低信息泄露风险。

支持的文件类型：

* **Office OpenXML**：`.docx .xlsx .pptx`（删除 `docProps/*`）
* **OpenDocument**：`.odt .ods .odp`（删除 `meta.xml`）
* **图片**：`.jpg/.jpeg .png`（重新编码，丢弃 EXIF/XMP 等元数据）
* **PDF**：可选支持（需 `pdfcpu` 依赖，清理 Info Dict 与 XMP 元数据）

---

## 安装与构建

1. 克隆或保存源码为 `main.go`

2. 构建可执行文件：

   ```bash
   go build -o  DataMasking.exe main.go
   ```

3. （可选）启用 PDF 支持：

   ```bash
   go get github.com/pdfcpu/pdfcpu@latest
   ```

   然后将源码中 `scrubPDFWithPDFCPU` 替换为注释里的 pdfcpu 真实实现，再 `go build`。

---

## 使用方法

### 基本用法

```bash
DataMasking --path <文件或目录路径> [选项...]
```

### 参数说明

| 参数           | 默认值     | 说明                               |
| ------------ | ------- | -------------------------------- |
| `--path`     | (必填)    | 待处理的文件或目录路径                      |
| `--backup`   | `true`  | 是否保留 `.bak` 备份                   |
| `--dry-run`  | `false` | 演示模式：只显示将处理的文件，不做修改              |
| `--workers`  | CPU 核数  | 并发处理协程数                          |
| `--with-pdf` | `false` | 启用 PDF 脱敏（需 pdfcpu）              |
| `--include`  | 空       | 仅处理这些扩展名（逗号分隔，如 `docx,xlsx,pdf`） |
| `--exclude`  | 空       | 排除这些扩展名                          |
| `-v`         | `false` | 输出详细日志                           |

---

## 使用示例

1. **处理单个文件**

   ```bash
   DataMasking --path "D:\报告\报告.docx"
   ```

2. **递归处理目录**

   ```bash
   DataMasking --path "D:\项目资料"
   ```

3. **仅处理 Office 文档**

   ```bash
   DataMasking --path "D:\资料" --include docx,xlsx,pptx
   ```

4. **排除图片文件**

   ```bash
   DataMasking --path "D:\资料" --exclude jpg,png
   ```

5. **启用 PDF 脱敏**

   ```bash
   DataMasking --path "D:\PDF库" --with-pdf
   ```

6. **演示模式**（不会改动文件）

   ```bash
   DataMasking --path "D:\资料" --dry-run
   ```

---

## 工作原理

* **Office / OpenDocument**
  文件本质是 ZIP 包，工具会重写压缩包，删除其中的 `docProps/*`（Office）或 `meta.xml`（OpenDocument）。

* **图片 (JPEG/PNG)**
  使用 Go 原生 `image` 解码，再重新编码输出，天然去掉 EXIF/XMP 信息。

* **PDF（可选）**
  使用 `pdfcpu` 库清理 Info Dict、XMP 元数据，并优化文档。

---

## 常见问题 (FAQ)

### Q1: 为什么提示 “文件被占用”？

A: Windows 下若文件正在被 **Word/Excel/预览器** 打开，会导致替换失败。
请关闭相关程序，或将文件复制到临时目录后再处理。

### Q2: 会不会影响文档内容？

A: 不会。

* Office/OpenDocument：只删除元数据文件，不修改正文内容。
* 图片：重新编码后图像内容保持不变，但 JPEG 会有一次有损压缩（默认质量 95）。
* PDF：仅清理元数据信息。

### Q3: 处理后的文件能正常打开吗？

A: 可以。格式保持一致，只是去除了附带的文档属性/元数据。

### Q4: 如何避免 `.bak` 文件越来越多？

A: 可以关闭备份功能：

```bash
DataMasking --path "D:\资料" --backup=false
```

---

## 注意事项

* **批量处理前请关闭 Word/Excel/PPT 等编辑器**，避免文件占用。
* 建议先用 `--dry-run` 查看待处理的文件清单。
* 若要长期使用，可将 `DataMasking.exe` 放入系统 PATH。

---

