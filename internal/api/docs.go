// 本文件提供内置 API 文档页：自包含 HTML（无外部资源），随服务一起分发，
// 页面内的“试一下”通过同源 fetch 直接调用真实接口。
package api

import (
	_ "embed"
	"net/http"
)

// docsHTML 是内嵌的 API 说明文档页。
//
//go:embed docs.html
var docsHTML []byte

// registerDocs 注册文档路由：/docs 返回文档页，/ 重定向到 /docs。
func registerDocs(mux *http.ServeMux) {
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(docsHTML)
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs", http.StatusFound)
	})
}
