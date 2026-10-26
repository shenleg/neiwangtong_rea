// 本文件初始化官方 Swagger UI；只加载同源规范，禁用外部校验、凭据及自动重定向。
"use strict";

window.addEventListener("DOMContentLoaded", () => {
  SwaggerUIBundle({
    url: "/openapi.json",
    dom_id: "#swagger-ui",
    layout: "BaseLayout",
    validatorUrl: null,
    queryConfigEnabled: false,
    tryItOutEnabled: false,
    supportedSubmitMethods: ["get", "post"],
    persistAuthorization: false,
    withCredentials: false,
    deepLinking: true,
    docExpansion: "none",
    defaultModelsExpandDepth: 0,
    requestInterceptor(request) {
      const target = new URL(request.url, window.location.href);
      if (target.origin !== window.location.origin || target.username || target.password) {
        throw new Error("文档只允许请求本站接口，不发送外部请求或 URL 凭据");
      }
      // 307/308 会重复提交 POST，因此让 fetch 直接报错，而不是自动跟随。
      request.redirect = "error";
      request.credentials = "omit";
      for (const name of Object.keys(request.headers)) {
        if (name.toLowerCase() === "authorization") {
          delete request.headers[name];
        }
      }
      return request;
    },
  });
});
