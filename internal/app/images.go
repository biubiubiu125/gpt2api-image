package app

import (
	"archive/zip"
	"crypto/md5"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func (s *Server) saveImage(r *http.Request, data []byte) (string, string, error) {
	if s.imageSaver != nil {
		return s.imageSaver(r, data)
	}
	return s.saveImageWithBaseURL(s.baseURL(r), data)
}

func (s *Server) saveImageWithBaseURL(baseURL string, data []byte) (string, string, error) {
	s.maybeCleanupOldImages()
	now := time.Now()
	sum := md5.Sum(data)
	relDir := filepath.Join(now.Format("2006"), now.Format("01"), now.Format("02"))
	name := fmt.Sprintf("%d_%s_%x%s", now.UnixNano(), randID(6), sum, storedImageExtension(data))
	rel := filepath.ToSlash(filepath.Join(relDir, name))
	path := filepath.Join(s.imagesDir, relDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", "", err
	}
	return rel, strings.TrimRight(baseURL, "/") + "/images/" + rel, nil
}

func (s *Server) cleanupSavedImages(rels []string) {
	for _, rel := range rels {
		path, err := s.imagePath(rel)
		if err == nil {
			_ = os.Remove(path)
		}
	}
}

func (s *Server) cleanupSavedImageResults(rels []string) {
	s.cleanupSavedImages(rels)
	if len(rels) == 0 || s.store == nil {
		return
	}
	set := map[string]bool{}
	for _, rel := range rels {
		if cleaned := relClean(rel); cleaned != "" {
			set[cleaned] = true
		}
	}
	if len(set) == 0 {
		return
	}
	_ = s.store.UpdateOwners(func(owners map[string]string) map[string]string {
		for rel := range set {
			delete(owners, rel)
		}
		return owners
	})
	_ = s.store.UpdatePrompts(func(prompts map[string]map[string]any) map[string]map[string]any {
		for rel := range set {
			delete(prompts, rel)
		}
		return prompts
	})
}

func (s *Server) recordImageMetadata(id *Identity, rel, prompt string, isEdit bool) error {
	if err := s.recordOwner(id, rel); err != nil {
		return err
	}
	return s.recordPrompt(rel, prompt, isEdit)
}

func (s *Server) recordOwner(id *Identity, rel string) error {
	if id == nil || s.store == nil {
		return nil
	}
	return s.store.UpdateOwners(func(owners map[string]string) map[string]string {
		owners[rel] = id.ID
		return owners
	})
}
func (s *Server) recordPrompt(rel, prompt string, isEdit bool) error {
	if s.store == nil {
		return nil
	}
	return s.store.UpdatePrompts(func(ps map[string]map[string]any) map[string]map[string]any {
		ps[rel] = map[string]any{"prompt": prompt, "is_edit": isEdit, "created_at": time.Now().Unix()}
		return ps
	})
}
func (s *Server) maybeCleanupOldImages() {
	s.imageCleanupMu.Lock()
	if time.Since(s.lastImageCleanup) < time.Hour {
		s.imageCleanupMu.Unlock()
		return
	}
	s.lastImageCleanup = time.Now()
	s.imageCleanupMu.Unlock()
	s.cleanupOldImages()
}
func (s *Server) cleanupOldImages() int {
	days := s.cfg.ImageRetentionDays
	if days <= 0 {
		days = 30
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	protected := map[string]bool{}
	if s.cfg.CleanupProtectUserImages {
		for rel, owner := range s.store.LoadOwners() {
			owner = strings.ToLower(strings.TrimSpace(owner))
			if rel = relClean(rel); rel != "" && owner != "" && owner != "admin" && owner != "__admin__" {
				protected[rel] = true
			}
		}
	}
	removed := 0
	_ = filepath.WalkDir(s.imagesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		st, err := d.Info()
		if err != nil || st.ModTime().After(cutoff) {
			return nil
		}
		rel, err := filepath.Rel(s.imagesDir, path)
		if err == nil && protected[filepath.ToSlash(rel)] {
			return nil
		}
		if os.Remove(path) == nil {
			removed++
		}
		return nil
	})
	// 清理空目录，深目录优先
	dirs := []string{}
	_ = filepath.WalkDir(s.imagesDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && path != s.imagesDir {
			dirs = append(dirs, path)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = os.Remove(d)
	}
	if removed > 0 && s.logSvc != nil {
		s.logSvc.add("system", "清理旧图片", map[string]any{"removed": removed, "retention_days": days})
	}
	return removed
}

func relFromURL(u string) string {
	if i := strings.Index(u, "/images/"); i >= 0 {
		return relClean(u[i+8:])
	}
	return relClean(u)
}

func safeImageRel(value string) (string, error) {
	rel := relClean(value)
	if rel == "" {
		return "", fmt.Errorf("image path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("invalid image path")
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(rel)))
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("invalid image path")
	}
	return cleaned, nil
}

func (s *Server) imagePath(rel string) (string, error) {
	cleaned, err := safeImageRel(rel)
	if err != nil {
		return "", err
	}
	base := filepath.Clean(s.imagesDir)
	target := filepath.Clean(filepath.Join(base, filepath.FromSlash(cleaned)))
	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid image path")
	}
	return target, nil
}

type imageFilter struct {
	Owner     string
	StartDate string
	EndDate   string
}

func newImageFilter(owner, startDate, endDate string) (imageFilter, error) {
	start, err := normalizeImageFilterDate(startDate)
	if err != nil {
		return imageFilter{}, err
	}
	end, err := normalizeImageFilterDate(endDate)
	if err != nil {
		return imageFilter{}, err
	}
	if start != "" && end != "" && start > end {
		return imageFilter{}, fmt.Errorf("start_date cannot be after end_date")
	}
	return imageFilter{Owner: strings.TrimSpace(owner), StartDate: start, EndDate: end}, nil
}

func normalizeImageFilterDate(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	t, err := time.Parse("2006-01-02", value)
	if err != nil {
		return "", fmt.Errorf("invalid image date %q", value)
	}
	return t.Format("2006-01-02"), nil
}

func (f imageFilter) empty() bool {
	return f.Owner == "" && f.StartDate == "" && f.EndDate == ""
}

func (f imageFilter) matches(owner string, modTime time.Time) bool {
	if f.Owner != "" {
		switch f.Owner {
		case "__unowned__":
			if owner != "" {
				return false
			}
		case "__admin__":
			if owner != "admin" {
				return false
			}
		default:
			if owner != f.Owner {
				return false
			}
		}
	}
	date := modTime.Format("2006-01-02")
	if f.StartDate != "" && date < f.StartDate {
		return false
	}
	if f.EndDate != "" && date > f.EndDate {
		return false
	}
	return true
}

func isStoredImageFile(path string) bool {
	return isStoredImageExtension(filepath.Ext(path))
}

func isStoredImageExtension(ext string) bool {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".bmp", ".tif", ".tiff":
		return true
	default:
		return false
	}
}

func storedImageExtension(data []byte) string {
	mediaType := strings.ToLower(downloadedImageMIME(data))
	switch mediaType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/avif":
		return ".avif"
	case "image/bmp":
		return ".bmp"
	case "image/tiff":
		return ".tif"
	}
	if strings.HasPrefix(mediaType, "image/") {
		if exts, err := mime.ExtensionsByType(mediaType); err == nil {
			for _, ext := range exts {
				if isStoredImageExtension(ext) {
					return strings.ToLower(ext)
				}
			}
		}
	}
	return ".png"
}

func (s *Server) walkStoredImages(fn func(path, rel string, st os.FileInfo) error) error {
	return filepath.WalkDir(s.imagesDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !isStoredImageFile(path) {
			return nil
		}
		st, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(s.imagesDir, path)
		if err != nil {
			return nil
		}
		return fn(path, filepath.ToSlash(rel), st)
	})
}

func (s *Server) listImages(r *http.Request, filter imageFilter) map[string]any {
	owners := s.store.LoadOwners()
	prompts := s.store.LoadPrompts()
	tags := s.store.LoadTags()
	items := []map[string]any{}
	_ = s.walkStoredImages(func(path string, rel string, st os.FileInfo) error {
		owner := owners[rel]
		if !filter.matches(owner, st.ModTime()) {
			return nil
		}
		pr := prompts[rel]
		items = append(items, map[string]any{"rel": rel, "path": rel, "name": filepath.Base(path), "date": st.ModTime().Format("2006-01-02"), "size": st.Size(), "url": s.baseURL(r) + "/images/" + rel, "thumbnail_url": s.baseURL(r) + "/image-thumbnails/" + rel, "created_at": st.ModTime().Format(time.RFC3339), "tags": tags[rel], "owner_id": owner, "is_admin_owner": owner == "admin", "prompt": strAny(pr["prompt"], "")})
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return strAny(items[i]["created_at"], "") > strAny(items[j]["created_at"], "") })
	return map[string]any{"items": items}
}
func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	filter, err := newImageFilter(r.URL.Query().Get("owner"), r.URL.Query().Get("start_date"), r.URL.Query().Get("end_date"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, s.listImages(r, filter))
}
func (s *Server) handleMyImages(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	filter, err := newImageFilter(id.ID, r.URL.Query().Get("start_date"), r.URL.Query().Get("end_date"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, s.listImages(r, filter))
}
func (s *Server) handleImageOwners(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	owners := s.store.LoadOwners()
	counts := map[string]int{}
	_ = s.walkStoredImages(func(_ string, rel string, _ os.FileInfo) error {
		owner := owners[rel]
		if owner == "" {
			counts["__unowned__"]++
		} else {
			counts[owner]++
		}
		return nil
	})
	items := []map[string]any{{"id": "__admin__", "name": "主密钥", "deleted": false, "count": counts["admin"]}, {"id": "__unowned__", "name": "未归属", "deleted": false, "count": counts["__unowned__"]}}
	for _, k := range s.store.LoadAuthKeys() {
		items = append(items, map[string]any{"id": k.ID, "name": k.Name, "deleted": false, "count": counts[k.ID]})
	}
	writeJSON(w, 200, map[string]any{"items": items})
}
func (s *Server) handleImageDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireIdentity(w, r)
	if !ok {
		return
	}
	var b struct {
		Paths       []string `json:"paths"`
		StartDate   string   `json:"start_date"`
		EndDate     string   `json:"end_date"`
		Owner       string   `json:"owner"`
		Tags        []string `json:"tags"`
		AllMatching bool     `json:"all_matching"`
	}
	if !readBody(w, r, &b) {
		return
	}
	owners := s.store.LoadOwners()
	paths := append([]string{}, b.Paths...)
	if b.AllMatching {
		owner := strings.TrimSpace(b.Owner)
		if id.Role != "admin" {
			owner = id.ID
		}
		requiredTags := normalizeImageFilterTags(b.Tags)
		filter, err := newImageFilter(owner, b.StartDate, b.EndDate)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if filter.empty() && len(requiredTags) == 0 {
			writeErr(w, 400, "at least one image filter is required")
			return
		}
		paths = s.matchingImagePaths(owners, s.store.LoadTags(), filter, requiredTags)
	}
	removed := 0
	removedOwners := map[string]bool{}
	for _, p := range paths {
		rel, err := safeImageRel(p)
		if err != nil {
			continue
		}
		if id.Role != "admin" && owners[rel] != id.ID {
			continue
		}
		path, err := s.imagePath(rel)
		if err != nil {
			continue
		}
		if os.Remove(path) == nil {
			removed++
			removedOwners[rel] = true
		}
	}
	if len(removedOwners) > 0 {
		s.deleteImageMetadata(removedOwners)
	}
	writeJSON(w, 200, map[string]any{"removed": removed})
}

func normalizeImageFilterTags(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func imageTagsMatch(actual []string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	have := map[string]bool{}
	for _, tag := range actual {
		have[tag] = true
	}
	for _, tag := range required {
		if !have[tag] {
			return false
		}
	}
	return true
}

func (s *Server) matchingImagePaths(owners map[string]string, tags map[string][]string, filter imageFilter, requiredTags []string) []string {
	paths := []string{}
	_ = s.walkStoredImages(func(_ string, rel string, st os.FileInfo) error {
		if filter.matches(owners[rel], st.ModTime()) && imageTagsMatch(tags[rel], requiredTags) {
			paths = append(paths, rel)
		}
		return nil
	})
	return paths
}

func (s *Server) deleteImageMetadata(rels map[string]bool) {
	_ = s.store.UpdateOwners(func(owners map[string]string) map[string]string {
		for rel := range rels {
			delete(owners, rel)
		}
		return owners
	})
	_ = s.store.UpdatePrompts(func(prompts map[string]map[string]any) map[string]map[string]any {
		for rel := range rels {
			delete(prompts, rel)
		}
		return prompts
	})
	_ = s.store.UpdateTags(func(tags map[string][]string) map[string][]string {
		for rel := range rels {
			delete(tags, rel)
		}
		return tags
	})
}
func (s *Server) handleImageDownload(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	var b struct {
		Paths []string `json:"paths"`
	}
	if !readBody(w, r, &b) {
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=images.zip")
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, p := range b.Paths {
		rel, err := safeImageRel(p)
		if err != nil {
			continue
		}
		path, err := s.imagePath(rel)
		if err != nil {
			continue
		}
		src, err := os.Open(path)
		if err != nil {
			continue
		}
		f, err := zw.Create(filepath.Base(rel))
		if err == nil {
			_, _ = io.Copy(f, src)
		}
		_ = src.Close()
	}
}
func (s *Server) handleImageDownloadSingle(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	rel, err := safeImageRel(strings.TrimPrefix(r.URL.Path, "/api/images/download/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, err := s.imagePath(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}
func (s *Server) handleStoredImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rel, err := safeImageRel(strings.TrimPrefix(r.URL.Path, "/images/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	path, err := s.imagePath(rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || !isStoredImageFile(path) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}
func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/image-thumbnails/")
	s.serveThumbnail(w, r, rel)
}
func (s *Server) handleImageTags(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if r.Method == http.MethodGet {
		tags := s.store.LoadTags()
		set := map[string]bool{}
		for _, ts := range tags {
			for _, t := range ts {
				set[t] = true
			}
		}
		arr := []string{}
		for t := range set {
			arr = append(arr, t)
		}
		sort.Strings(arr)
		writeJSON(w, 200, map[string]any{"tags": arr})
		return
	}
	if r.Method == http.MethodPost {
		var b struct {
			Path string   `json:"path"`
			Tags []string `json:"tags"`
		}
		if !readBody(w, r, &b) {
			return
		}
		if err := s.store.UpdateTags(func(tags map[string][]string) map[string][]string {
			tags[relClean(b.Path)] = b.Tags
			return tags
		}); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "tags": b.Tags})
		return
	}
	writeErr(w, 405, "method not allowed")
}
func (s *Server) handleImageTagDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	tag, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/images/tags/"))
	n := 0
	if err := s.store.UpdateTags(func(tags map[string][]string) map[string][]string {
		for rel, ts := range tags {
			out := []string{}
			for _, t := range ts {
				if t == tag {
					n++
				} else {
					out = append(out, t)
				}
			}
			tags[rel] = out
		}
		return tags
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "removed_from": n})
}
