package api

import (
	"io"
	"mime"
	"net/http"
	"path"
	"strings"

	"mesh-server/httpx"
)

// handleIndex serves the console entry document.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.serveIndex(w, r)
}

// handleAsset serves one file of the embedded build. An unknown path is a 404:
// 控制台没有客户端路由，回落到入口文档只会在资源换名后把 404 藏成一个 200 的
// text/html，浏览器按 MIME 拒绝执行，表现为白屏。
func (s *Server) handleAsset(w http.ResponseWriter, r *http.Request) {
	name := path.Clean(strings.TrimPrefix(r.URL.Path, "/admin/"))
	// index.html 不是指纹文件名：缓存一年会让升级后的浏览器继续用旧入口文档去
	// 加载已经不存在的旧 asset，所以它和 /admin、/admin/ 共用无缓存的入口。
	if s.static == nil || name == "." || name == "index.html" {
		s.serveIndex(w, r)
		return
	}
	if name == ".." || strings.HasPrefix(name, "../") {
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	file, err := s.static.Open(name)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	w.Header().Set("Content-Type", contentTypeOf(name))
	// Vite fingerprints the asset names, so a URL never changes its bytes.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, name, info.ModTime(), seeker)
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if len(s.index) == 0 {
		httpx.WriteError(w, http.StatusNotFound, "not_found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.index)
}

func contentTypeOf(name string) string {
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}
