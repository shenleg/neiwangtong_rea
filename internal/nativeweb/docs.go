// 本文件注册随程序内嵌的OpenAPI和Swagger UI资源；文档不访问外部CDN或在线校验器。
package nativeweb

import (
	"net/http"
)

func registerDocs(mux *http.ServeMux) error {
	for path, file := range map[string]string{
		"/docs":                            "docs/index.html",
		"/swagger/index.html":              "docs/index.html",
		"/openapi.json":                    "docs/openapi.json",
		"/docs/init.js":                    "docs/init.js",
		"/docs/style.css":                  "docs/style.css",
		"/swagger-ui/swagger-ui-bundle.js": "swagger/swagger-ui-bundle.js",
		"/swagger-ui/swagger-ui.css":       "swagger/swagger-ui.css",
	} {
		path, file := path, file
		data, err := assets.ReadFile("assets/" + file)
		if err != nil {
			return err
		}
		contentType := "text/html; charset=utf-8"
		switch file {
		case "docs/openapi.json":
			contentType = "application/json; charset=utf-8"
		case "docs/init.js", "swagger/swagger-ui-bundle.js":
			contentType = "text/javascript; charset=utf-8"
		case "docs/style.css", "swagger/swagger-ui.css":
			contentType = "text/css; charset=utf-8"
		}
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write(data)
		})
	}
	return nil
}
