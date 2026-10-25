// Package assets 内嵌模板、静态资源与 SKU 映射，构建产物为单二进制。
package assets

import "embed"

//go:embed templates
var Templates embed.FS

//go:embed static
var Static embed.FS

//go:embed sku_product_mapping.json
var SKUMapping []byte
