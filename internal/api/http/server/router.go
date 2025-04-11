package server

import "net/http"

type HTTPRouter http.ServeMux

func (*HTTPRouter) PathValue(r *http.Request, paramName string) string {
	return r.PathValue(paramName)
}

func (router *HTTPRouter) HandleRoute(method, pathPattern string, h http.Handler) {
	(*http.ServeMux)(router).Handle(method+" "+pathPattern, h)
}

func (router *HTTPRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	(*http.ServeMux)(router).ServeHTTP(w, r)
}
