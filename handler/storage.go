package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/tigerowo/infinite-canvas/model"
	"github.com/tigerowo/infinite-canvas/service"
)

// StorageConfig 返回公开存储配置。
func StorageConfig(w http.ResponseWriter, r *http.Request) {
	config, err := service.PublicStorageConfig()
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, config)
}

// SaveUserStorageProvider 保存用户配置的存储提供商。
func SaveUserStorageProvider(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Provider service.UserStorageProviders `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		Fail(w, "配置内容格式错误")
		return
	}
	config, err := service.SaveCurrentUserStorageProvider(r.Context(), request.Provider)
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, config)
}

// MeasureUserStorageProvider 统计用户存储提供商的已用容量。
func MeasureUserStorageProvider(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Provider service.StorageObjectProviderInput `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		Fail(w, "配置内容格式错误")
		return
	}
	result, err := service.MeasureUserStorageProvider(r.Context(), request.Provider)
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, result)
}

// UploadFile 上传文件到对象存储。
func UploadFile(w http.ResponseWriter, r *http.Request) {
	file, header, err := r.FormFile("file")
	if err != nil {
		Fail(w, "请选择要上传的文件")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		FailError(w, err)
		return
	}
	contentType := header.Header.Get("Content-Type")
	if strings.TrimSpace(contentType) == "" {
		contentType = http.DetectContentType(data)
	}
	var provider *service.StorageObjectProviderInput
	if raw := strings.TrimSpace(r.FormValue("provider")); raw != "" {
		var parsed service.StorageObjectProviderInput
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			Fail(w, "用户对象存储配置格式错误")
			return
		}
		provider = &parsed
	}
	object, err := service.UploadStorageObjectWithProvider(r.Context(), header.Filename, contentType, data, provider)
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, object)
}

// RegisterDirectFile 登记浏览器已直传至用户 WebDAV 的文件。
func RegisterDirectFile(w http.ResponseWriter, r *http.Request) {
	var request service.DirectStorageObjectInput
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		Fail(w, "文件信息格式错误")
		return
	}
	object, err := service.RegisterDirectStorageObject(r.Context(), request)
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, object)
}

// DeleteFile 删除文件。
func DeleteFile(w http.ResponseWriter, r *http.Request, id string) {
	var request struct {
		Provider *service.StorageObjectProviderInput `json:"provider"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&request)
	}
	if err := service.DeleteStorageObject(r.Context(), id, request.Provider); err != nil {
		FailError(w, err)
		return
	}
	OK(w, true)
}

// DeleteDirectFileRecord 删除浏览器已直接删除的 WebDAV 文件索引。
func DeleteDirectFileRecord(w http.ResponseWriter, r *http.Request, id string) {
	if err := service.DeleteDirectStorageObjectRecord(r.Context(), id); err != nil {
		FailError(w, err)
		return
	}
	OK(w, true)
}

// downloadStorageObject 是公开文件读取路径的数据获取函数，默认走 service 实现；
// 声明为变量以便测试注入（与 proxyImageHTTPClient 同模式，不改变生产行为）。
var downloadStorageObject = service.DownloadStorageObject

// FileContent 获取文件内容。
func FileContent(w http.ResponseWriter, r *http.Request, id string) {
	// R1 收敛：房间共享媒体不得经公开对象 ID 路径读取，必须走成员鉴权接口。
	if err := service.AuthorizeFileRead(id); err != nil {
		writeFileReadDenied(w, err)
		return
	}
	download, err := downloadStorageObject(id, r.Header.Get("Range"))
	if err != nil {
		FailError(w, err)
		return
	}
	defer download.Stream.Close()
	// 存储对象的 MIME 由上传方声明、不可信：一律禁止嗅探；主动内容（SVG/HTML/XML/JS）
	// 降级为通用二进制并强制附件下载，避免经公开文件路径形成存储型 XSS。
	w.Header().Set("X-Content-Type-Options", "nosniff")
	contentType, inline := downloadResponseContentType(download.Object.MimeType)
	w.Header().Set("Content-Type", contentType)
	if !inline {
		w.Header().Set("Content-Disposition", contentDispositionAttachment(objectDownloadFilename(download.Object)))
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if download.AcceptRanges {
		w.Header().Set("Accept-Ranges", "bytes")
	}
	if download.ContentRange != "" {
		w.Header().Set("Content-Range", download.ContentRange)
	}
	if download.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(download.ContentLength, 10))
	}
	if download.StatusCode != 0 {
		w.WriteHeader(download.StatusCode)
	}
	_, _ = io.Copy(w, download.Stream)
}

// FileInfo 获取文件元数据。
func FileInfo(w http.ResponseWriter, r *http.Request, id string) {
	// R1 收敛：房间共享媒体不得经公开对象 ID 路径读取元数据。
	if err := service.AuthorizeFileRead(id); err != nil {
		writeFileReadDenied(w, err)
		return
	}
	object, err := service.StorageObjectInfo(id)
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, object)
}

// writeFileReadDenied 把公开文件读取路径的拒绝结果映射为 404，不泄漏资源是否存在。
func writeFileReadDenied(w http.ResponseWriter, err error) {
	if errors.Is(err, service.ErrMediaNotFound) {
		FailWithStatus(w, http.StatusNotFound, err.Error())
		return
	}
	FailError(w, err)
}

// downloadResponseContentType 收敛公开文件下载与图片代理的响应类型：空类型与主动内容
// （SVG/HTML/XML/JS）一律降级为通用二进制并要求附件下载，其余类型原样返回。
// 返回 (响应 Content-Type, 是否允许同源内联渲染)。绝不回显可用于执行脚本的声明类型。
func downloadResponseContentType(mimeType string) (string, bool) {
	contentType := strings.TrimSpace(mimeType)
	if contentType == "" || service.ActiveContentMimeType(contentType) {
		return "application/octet-stream", false
	}
	return contentType, true
}

// objectDownloadFilename 取存储对象的下载文件名（对象键基名，缺省回退对象 ID）。
func objectDownloadFilename(object model.StorageObject) string {
	if name := path.Base(strings.TrimSpace(object.ObjectKey)); name != "" && name != "." && name != "/" {
		return name
	}
	return object.ID
}

// AdminMeasureStorageProvider 管理员统计存储容量。
func AdminMeasureStorageProvider(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Index    int                    `json:"index"`
		Provider *model.StorageProvider `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		Fail(w, "配置内容格式错误")
		return
	}
	result, err := service.MeasureAdminStorageProvider(request.Index, request.Provider)
	if err != nil {
		FailError(w, err)
		return
	}
	OK(w, result)
}

// proxyImageHTTPClient 是图片代理使用的出网客户端，默认走 SSRF 白名单客户端
// （service.SafeProxyHTTPClient）；声明为变量以便测试替换（与 service.SchoolResultDownloader 同模式）。
var proxyImageHTTPClient = service.SafeProxyHTTPClient

// ProxyImage 代理图片请求（解决跨域和机器人检测问题）。
func ProxyImage(w http.ResponseWriter, r *http.Request) {
	targetURL := r.URL.Query().Get("url")
	if targetURL == "" {
		Fail(w, "url 参数不能为空")
		return
	}
	if !strings.HasPrefix(targetURL, "http://") && !strings.HasPrefix(targetURL, "https://") {
		Fail(w, "无效的 url")
		return
	}
	client := proxyImageHTTPClient()
	req, err := http.NewRequest(http.MethodGet, targetURL, nil)
	if err != nil {
		FailError(w, err)
		return
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")

	resp, err := client.Do(req)
	if err != nil {
		FailWithStatus(w, http.StatusBadGateway, "代理图片请求失败")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		FailWithStatus(w, http.StatusBadGateway, "代理图片请求失败: "+resp.Status)
		return
	}
	contentType, inline := downloadResponseContentType(resp.Header.Get("Content-Type"))
	w.Header().Set("Content-Type", contentType)
	// 上游声明的 Content-Type 不可信：一律禁止嗅探；主动内容降级为通用二进制并强制
	// 附件下载，避免把远端 HTML/SVG 反射为同源 XSS。
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if !inline {
		w.Header().Set("Content-Disposition", "attachment")
	}
	if inline && strings.HasPrefix(contentType, "image/") {
		data, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			FailWithStatus(w, http.StatusBadGateway, "代理图片请求失败")
			return
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(data)
		return
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
