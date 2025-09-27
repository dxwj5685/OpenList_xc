package czk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

type CZK struct {
	model.Storage
	Addition
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	client       *resty.Client
}

func (d *CZK) Config() driver.Config {
	return config
}

func (d *CZK) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *CZK) Init(ctx context.Context) error {
	d.client = resty.New()
	
	// 设置全局User-Agent
	d.client.SetHeader("User-Agent", "openlist")
	
	// 获取访问令牌
	if err := d.authenticate(); err != nil {
		return err
	}
	
	return nil
}

func (d *CZK) Drop(ctx context.Context) error {
	return nil
}

func (d *CZK) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if err := d.refreshTokenIfNeeded(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	url := fmt.Sprintf("https://pan.szczk.top/czkapi/list_files?folder_id=%s", dir.GetID())
	resp, err := d.client.R().
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		Get(url)

	if err != nil {
		return nil, fmt.Errorf("failed to send list request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("failed to list files with status %d: %s", resp.StatusCode(), resp.String())
	}

	// 解析响应并返回文件列表
	var listResp map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &listResp); err != nil {
		return nil, fmt.Errorf("failed to parse file list response: %w", err)
	}

	// 检查响应中是否有错误信息
	if status, ok := listResp["status"].(float64); ok && int64(status) != 200 {
		message := "unknown error"
		if msg, ok := listResp["message"].(string); ok {
			message = msg
		}
		return nil, fmt.Errorf("list files API error: status=%d, message=%s", int64(status), message)
	}

	// 从响应中提取文件数据
	var files []File
	if data, ok := listResp["data"].(map[string]interface{}); ok {
		if filesData, ok := data["files"].([]interface{}); ok {
			for _, fileData := range filesData {
				if fileMap, ok := fileData.(map[string]interface{}); ok {
					file := File{}
					if id, ok := fileMap["id"].(string); ok {
						file.ID = id
					}
					if name, ok := fileMap["name"].(string); ok {
						file.Name = name
					}
					if size, ok := fileMap["size"].(float64); ok {
						file.Size = int64(size)
					}
					if modified, ok := fileMap["modified"].(string); ok {
						file.Modified = modified
					}
					if isFolder, ok := fileMap["is_folder"].(bool); ok {
						file.IsFolder = isFolder
					}
					files = append(files, file)
				}
			}
		}
	}

	var objs []model.Obj
	for _, file := range files {
		// 解析文件修改时间
		var modified time.Time
		if file.Modified != "" {
			// 尝试几种常见的时间格式
			if t, err := time.Parse(time.RFC3339, file.Modified); err == nil {
				modified = t
			} else if t, err := time.Parse("2006-01-02 15:04:05", file.Modified); err == nil {
				modified = t
			} else {
				// 如果解析失败，使用当前时间
				modified = time.Now()
			}
		} else {
			modified = time.Now()
		}
		
		objs = append(objs, &model.Object{
			ID:       file.ID,
			Name:     file.Name,
			Size:     file.Size,
			Modified: modified,
			IsFolder: file.IsFolder,
		})
	}
	
	return objs, nil
}

func (d *CZK) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if err := d.refreshTokenIfNeeded(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	// 根据最新反馈，下载链接接口需要添加Authorization认证头部
	url := fmt.Sprintf("https://pan.szczk.top/czkapi/get_download_url?file_id=%s", file.GetID())
	resp, err := d.client.R().
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		Get(url)

	if err != nil {
		return nil, fmt.Errorf("failed to send get download link request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("failed to get download link with status %d: %s", resp.StatusCode(), resp.String())
	}

	// 解析响应并返回下载链接
	var downloadResp map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &downloadResp); err != nil {
		return nil, fmt.Errorf("failed to parse download link response: %w", err)
	}

	// 检查响应中是否有错误信息
	if status, ok := downloadResp["status"].(float64); ok && int64(status) != 200 {
		message := "unknown error"
		if msg, ok := downloadResp["message"].(string); ok {
			message = msg
		}
		return nil, fmt.Errorf("get download link API error: status=%d, message=%s", int64(status), message)
	}

	// 从响应中提取下载链接
	var downloadLink string
	if data, ok := downloadResp["data"].(map[string]interface{}); ok {
		// 尝试从不同字段获取下载链接
		if link, ok := data["download_link"].(string); ok && link != "" {
			downloadLink = link
		} else if url, ok := data["url"].(string); ok && url != "" {
			downloadLink = url
		}
	}
	
	if downloadLink == "" {
		return nil, fmt.Errorf("failed to get download link from response")
	}

	return &model.Link{
		URL: downloadLink,
	}, nil
}

func (d *CZK) authenticate() error {
	url := "https://pan.szczk.top/czkapi/authenticate"
	
	// 检查API密钥和密钥是否已设置
	if d.APIKey == "" || d.APISecret == "" {
		return fmt.Errorf("API key or secret not set")
	}
	
	// 设置请求超时时间
	d.client.SetTimeout(30 * time.Second)
	
	resp, err := d.client.R().
		SetHeader("x-api-key", d.APIKey).
		SetHeader("x-api-secret", d.APISecret).
		Get(url)

	if err != nil {
		return fmt.Errorf("failed to send auth request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("authentication failed with status %d: %s, response body: %s", resp.StatusCode(), resp.Status(), string(resp.Body()))
	}

	// 解析认证响应，获取access_token, refresh_token等
	var authResp AuthResp
	if err := json.Unmarshal(resp.Body(), &authResp); err != nil {
		return fmt.Errorf("failed to parse auth response: %w, response body: %s", err, string(resp.Body()))
	}
	
	// 检查API返回的状态码
	if authResp.Status != 200 {
		return fmt.Errorf("authentication API error: status=%d, message=%s", authResp.Status, authResp.Message)
	}
	
	// 检查是否获得了必要的令牌
	if authResp.Data.AccessToken == "" {
		return fmt.Errorf("authentication succeeded but no access token returned")
	}
	
	if authResp.Data.RefreshToken == "" {
		return fmt.Errorf("authentication succeeded but no refresh token returned")
	}
	
	// 更新令牌信息
	d.AccessToken = authResp.Data.AccessToken
	d.RefreshToken = authResp.Data.RefreshToken
	d.ExpiresAt = time.Now().Add(time.Duration(authResp.Data.ExpiresIn) * time.Second)
	
	return nil
}

func (d *CZK) refreshTokenIfNeeded() error {
	if time.Now().After(d.ExpiresAt) {
		// 尝试刷新令牌
		err := d.refreshToken()
		if err != nil {
			// 如果刷新令牌失败，尝试重新认证
			log.Printf("Failed to refresh token: %v, attempting to re-authenticate", err)
			return d.authenticate()
		}
	}
	return nil
}

func (d *CZK) refreshToken() error {
	url := "https://pan.szczk.top/czkapi/refresh_token"
	
	// 检查是否有有效的刷新令牌
	if d.RefreshToken == "" {
		// 如果没有刷新令牌，需要重新进行认证
		return fmt.Errorf("no refresh token available, need to re-authenticate")
	}
	
	// 创建表单数据
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	_ = writer.WriteField("refresh_token", d.RefreshToken)
	err := writer.Close()
	if err != nil {
		return fmt.Errorf("failed to create refresh token form: %w", err)
	}

	// 设置请求超时时间
	d.client.SetTimeout(30 * time.Second)
	
	resp, err := d.client.R().
		SetHeader("Content-Type", writer.FormDataContentType()).
		SetBody(payload.Bytes()).
		Post(url)

	if err != nil {
		return fmt.Errorf("failed to send refresh request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("token refresh failed with status %d: %s, response body: %s", resp.StatusCode(), resp.Status(), string(resp.Body()))
	}

	// 解析刷新令牌响应，更新access_token等
	var refreshResp RefreshResp
	if err := json.Unmarshal(resp.Body(), &refreshResp); err != nil {
		return fmt.Errorf("failed to parse refresh response: %w, response body: %s", err, string(resp.Body()))
	}
	
	// 检查API返回的状态码和成功标志
	// 当Success为true且Status为200时，表示刷新成功
	if !refreshResp.Success || refreshResp.Status != 200 {
		// 特别处理"需要提供刷新令牌"的错误
		if refreshResp.Message == "需要提供刷新令牌" {
			return fmt.Errorf("token refresh API error: status=%d, success=%t, message=%s, refresh token may be invalid or expired", refreshResp.Status, refreshResp.Success, refreshResp.Message)
		}
		return fmt.Errorf("token refresh API error: status=%d, success=%t, message=%s", refreshResp.Status, refreshResp.Success, refreshResp.Message)
	}
	
	// 更新访问令牌和过期时间
	// 根据API文档，当Success为true且Status为200时
	// Data.AccessToken会包含新令牌
	// Data.ExpiresIn会显示新令牌有效期
	// Data.TokenType会指定令牌类型(通常是"Bearer")
	d.AccessToken = refreshResp.Data.AccessToken
	d.ExpiresAt = time.Now().Add(time.Duration(refreshResp.Data.ExpiresIn) * time.Second)
	
	// 如果返回了新的刷新令牌，则更新它
	if refreshResp.Data.RefreshToken != "" {
		d.RefreshToken = refreshResp.Data.RefreshToken
	}
	
	return nil
}

// 以下方法为可选实现
func (d *CZK) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	if err := d.refreshTokenIfNeeded(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	url := "https://pan.szczk.top/czkapi/create_folder"
	
	// 创建表单数据
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	_ = writer.WriteField("parent_id", parentDir.GetID())
	_ = writer.WriteField("name", dirName)
	err := writer.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to create mkdir form: %w", err)
	}

	resp, err := d.client.R().
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetHeader("Content-Type", writer.FormDataContentType()).
		SetBody(payload.Bytes()).
		Post(url)

	if err != nil {
		return nil, fmt.Errorf("failed to send mkdir request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("failed to create folder with status %d: %s", resp.StatusCode(), resp.String())
	}

	// 解析响应
	var operationResp map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &operationResp); err != nil {
		return nil, fmt.Errorf("failed to parse create folder response: %w", err)
	}

	// 检查响应中是否有错误信息
	if status, ok := operationResp["status"].(float64); ok && int64(status) != 200 {
		message := "unknown error"
		if msg, ok := operationResp["message"].(string); ok {
			message = msg
		}
		return nil, fmt.Errorf("create folder API error: status=%d, message=%s", int64(status), message)
	}

	// 返回新创建的目录对象
	// 注意：这里应该根据实际API响应来构建对象
	// 目前我们创建一个基本的对象
	newObj := &model.Object{
		ID:       "", // 应该从响应中获取实际ID
		Name:     dirName,
		Size:     0,
		Modified: time.Now(),
		IsFolder: true,
	}
	
	return newObj, nil
}

func (d *CZK) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if err := d.refreshTokenIfNeeded(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	url := "https://pan.szczk.top/czkapi/move_item"
	
	// 创建表单数据，根据API示例使用正确的参数名
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	_ = writer.WriteField("id", srcObj.GetID())
	_ = writer.WriteField("type", func() string {
		if srcObj.IsDir() {
			return "folder"
		}
		return "file"
	}())
	_ = writer.WriteField("target_id", dstDir.GetID()) // 根据示例使用target_id而不是new_parent_id
	err := writer.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to create move form: %w", err)
	}

	resp, err := d.client.R().
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetHeader("Content-Type", writer.FormDataContentType()).
		SetBody(payload.Bytes()).
		Post(url)

	if err != nil {
		return nil, fmt.Errorf("failed to send move request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("failed to move item with status %d: %s", resp.StatusCode(), resp.String())
	}

	// 解析响应
	var operationResp map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &operationResp); err != nil {
		return nil, fmt.Errorf("failed to parse move response: %w", err)
	}

	// 检查响应中是否有错误信息
	if status, ok := operationResp["status"].(float64); ok && int64(status) != 200 {
		message := "unknown error"
		if msg, ok := operationResp["message"].(string); ok {
			message = msg
		}
		return nil, fmt.Errorf("move item API error: status=%d, message=%s", int64(status), message)
	}

	// 返回更新后的对象
	// 注意：这里应该根据实际API响应来构建对象
	// 目前我们简单地复制原对象并更新父目录
	newObj := &model.Object{
		ID:       srcObj.GetID(),
		Name:     srcObj.GetName(),
		Size:     srcObj.GetSize(),
		Modified: time.Now(),
		IsFolder: srcObj.IsDir(),
	}
	
	return newObj, nil
}

func (d *CZK) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	if err := d.refreshTokenIfNeeded(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	url := "https://pan.szczk.top/czkapi/rename_item"
	
	// 创建表单数据
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	_ = writer.WriteField("id", srcObj.GetID())
	_ = writer.WriteField("type", func() string {
		if srcObj.IsDir() {
			return "folder"
		}
		return "file"
	}())
	_ = writer.WriteField("new_name", newName)
	err := writer.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to create rename form: %w", err)
	}

	resp, err := d.client.R().
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetHeader("Content-Type", writer.FormDataContentType()).
		SetBody(payload.Bytes()).
		Post(url)

	if err != nil {
		return nil, fmt.Errorf("failed to send rename request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("failed to rename item with status %d: %s", resp.StatusCode(), resp.String())
	}

	// 解析响应
	var operationResp map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &operationResp); err != nil {
		return nil, fmt.Errorf("failed to parse rename response: %w", err)
	}

	// 检查响应中是否有错误信息
	if status, ok := operationResp["status"].(float64); ok && int64(status) != 200 {
		message := "unknown error"
		if msg, ok := operationResp["message"].(string); ok {
			message = msg
		}
		return nil, fmt.Errorf("rename item API error: status=%d, message=%s", int64(status), message)
	}

	// 返回更新后的对象
	// 注意：这里应该根据实际API响应来构建对象
	// 目前我们简单地复制原对象并更新名称
	newObj := &model.Object{
		ID:       srcObj.GetID(),
		Name:     newName,
		Size:     srcObj.GetSize(),
		Modified: time.Now(),
		IsFolder: srcObj.IsDir(),
	}
	
	return newObj, nil
}

func (d *CZK) Remove(ctx context.Context, obj model.Obj) error {
	if err := d.refreshTokenIfNeeded(); err != nil {
		return fmt.Errorf("failed to refresh token: %w", err)
	}

	url := "https://pan.szczk.top/czkapi/delete_item"
	
	// 创建表单数据
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	_ = writer.WriteField("id", obj.GetID())
	_ = writer.WriteField("type", func() string {
		if obj.IsDir() {
			return "folder"
		}
		return "file"
	}())
	err := writer.Close()
	if err != nil {
		return fmt.Errorf("failed to create delete form: %w", err)
	}

	resp, err := d.client.R().
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetHeader("Content-Type", writer.FormDataContentType()).
		SetBody(payload.Bytes()).
		Post(url)

	if err != nil {
		return fmt.Errorf("failed to send delete request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("failed to delete item with status %d: %s", resp.StatusCode(), resp.String())
	}

	// 解析响应
	var operationResp map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &operationResp); err != nil {
		return fmt.Errorf("failed to parse delete response: %w", err)
	}

	// 检查响应中是否有错误信息
	if status, ok := operationResp["status"].(float64); ok && int64(status) != 200 {
		message := "unknown error"
		if msg, ok := operationResp["message"].(string); ok {
			message = msg
		}
		return fmt.Errorf("delete item API error: status=%d, message=%s", int64(status), message)
	}

	return nil
}

func (d *CZK) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	if err := d.refreshTokenIfNeeded(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	// 初始化上传
	initURL := "https://pan.szczk.top/czkapi/first_upload"
	
	// 创建表单数据
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	_ = writer.WriteField("hash", "") // 简化处理，实际应计算文件hash
	_ = writer.WriteField("filename", file.GetName())
	_ = writer.WriteField("filesize", fmt.Sprintf("%d", file.GetSize()))
	_ = writer.WriteField("folder", dstDir.GetID())
	err := writer.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to create init upload form: %w", err)
	}

	resp, err := d.client.R().
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetHeader("Content-Type", writer.FormDataContentType()).
		SetBody(payload.Bytes()).
		Post(initURL)

	if err != nil {
		return nil, fmt.Errorf("failed to send init upload request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("failed to initialize upload with status %d: %s", resp.StatusCode(), resp.String())
	}

	// 解析初始化上传的响应
	var initResp map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &initResp); err != nil {
		return nil, fmt.Errorf("failed to parse upload init response: %w", err)
	}

	// 检查响应中是否有错误信息
	if status, ok := initResp["status"].(float64); ok && int64(status) != 200 {
		message := "unknown error"
		if msg, ok := initResp["message"].(string); ok {
			message = msg
		}
		return nil, fmt.Errorf("init upload API error: status=%d, message=%s", int64(status), message)
	}

	// 从初始化响应中提取需要的参数
	csrfToken := ""
	fileKey := ""
	
	if data, ok := initResp["data"].(map[string]interface{}); ok {
		if token, ok := data["csrf_token"].(string); ok {
			csrfToken = token
		}
		if key, ok := data["file_key"].(string); ok {
			fileKey = key
		}
	}

	// 检查必要参数是否存在
	if csrfToken == "" || fileKey == "" {
		return nil, fmt.Errorf("missing required parameters from init upload response: csrf_token=%s, file_key=%s", csrfToken, fileKey)
	}

	// 完成上传
	completeURL := "https://pan.szczk.top/czkapi/ok_upload"
	
	// 创建完成上传的表单数据
	completePayload := &bytes.Buffer{}
	completeWriter := multipart.NewWriter(completePayload)
	_ = completeWriter.WriteField("hash", "") // 简化处理，实际应使用文件hash
	_ = completeWriter.WriteField("filename", file.GetName())
	_ = completeWriter.WriteField("filesize", fmt.Sprintf("%d", file.GetSize()))
	_ = completeWriter.WriteField("csrf_token", csrfToken)
	_ = completeWriter.WriteField("file_key", fileKey)
	_ = completeWriter.WriteField("folder", dstDir.GetID())
	err = completeWriter.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to create complete upload form: %w", err)
	}

	completeResp, err := d.client.R().
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetHeader("Content-Type", completeWriter.FormDataContentType()).
		SetBody(completePayload.Bytes()).
		Post(completeURL)

	if err != nil {
		return nil, fmt.Errorf("failed to send complete upload request: %w", err)
	}

	if completeResp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("failed to complete upload with status %d: %s", completeResp.StatusCode(), completeResp.String())
	}

	// 解析完成上传的响应
	var completeRespData map[string]interface{}
	if err := json.Unmarshal(completeResp.Body(), &completeRespData); err != nil {
		return nil, fmt.Errorf("failed to parse upload complete response: %w", err)
	}

	// 检查响应中是否有错误信息
	if status, ok := completeRespData["status"].(float64); ok && int64(status) != 200 {
		message := "unknown error"
		if msg, ok := completeRespData["message"].(string); ok {
			message = msg
		}
		return nil, fmt.Errorf("complete upload API error: status=%d, message=%s", int64(status), message)
	}

	// 返回新创建的文件对象
	newObj := &model.Object{
		ID:       "", // 应该从响应中获取实际ID
		Name:     file.GetName(),
		Size:     file.GetSize(),
		Modified: time.Now(),
		IsFolder: false,
	}
	
	return newObj, nil
}

func (d *CZK) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	return nil, errs.NotImplement
}

func (d *CZK) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *CZK) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	return nil, errs.NotImplement
}

func (d *CZK) ArchiveDecompress(ctx context.Context, srcObj, dstDir model.Obj, args model.ArchiveDecompressArgs) ([]model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *CZK) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	return nil, errs.NotImplement
}

var _ driver.Driver = (*CZK)(nil)

// getStringValue 从interface{}中安全地提取字符串值
func getStringValue(val interface{}) string {
	if str, ok := val.(string); ok {
		return str
	}
	return ""
}
