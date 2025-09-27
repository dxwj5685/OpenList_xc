package czk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
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

	// 根据API文档，文件列表接口需要在URL中包含folder_id参数，并在请求头中携带Authorization
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
		log.Printf("CZK List: failed to parse file list response: %v, response body: %s", err, string(resp.Body()))
		return nil, fmt.Errorf("failed to parse file list response: %w", err)
	}

	// 记录响应内容用于调试
	log.Printf("CZK List response: %+v", listResp)

	// 检查响应中是否有错误信息
	if code, ok := listResp["code"].(float64); ok && int64(code) != 200 {
		message := "unknown error"
		if msg, ok := listResp["message"].(string); ok {
			message = msg
		}
		return nil, fmt.Errorf("list files API error: code=%d, message=%s", int64(code), message)
	}

	// 从响应中提取文件数据
	var objs []model.Obj

	// 根据API示例，正确的结构是 {code, message, data: {items: [], total_count}}
	if data, ok := listResp["data"].(map[string]interface{}); ok {
		if items, ok := data["items"].([]interface{}); ok {
			for _, itemData := range items {
				if itemMap, ok := itemData.(map[string]interface{}); ok {
					// 解析文件/文件夹信息
					id := ""
					if itemId, ok := itemMap["id"].(float64); ok {
						id = fmt.Sprintf("%.0f", itemId) // ID是数字，转换为字符串
					}

					name := ""
					if itemName, ok := itemMap["name"].(string); ok {
						name = itemName
					}

					size := int64(0)
					if itemSize, ok := itemMap["size"].(float64); ok {
						size = int64(itemSize)
					}

					isFolder := false
					if itemType, ok := itemMap["type"].(string); ok {
						isFolder = (itemType == "folder")
					}

					// 解析时间
					modifiedStr := ""
					if isFolder {
						if createdAt, ok := itemMap["created_at"].(string); ok {
							modifiedStr = createdAt
						}
					} else {
						if uploadedAt, ok := itemMap["uploaded_at"].(string); ok {
							modifiedStr = uploadedAt
						}
					}

					// 解析修改时间
					var modified time.Time
					if modifiedStr != "" {
						// 尝试解析时间格式 "2025-06-29 15:37:01"
						if t, err := time.Parse("2006-01-02 15:04:05", modifiedStr); err == nil {
							modified = t
						} else {
							// 如果解析失败，使用当前时间
							modified = time.Now()
						}
					} else {
						modified = time.Now()
					}

					obj := &model.Object{
						ID:       id,
						Name:     name,
						Size:     size,
						Modified: modified,
						IsFolder: isFolder,
					}

					objs = append(objs, obj)
				}
			}
		}
	}

	log.Printf("CZK List: successfully listed %d files", len(objs))
	return objs, nil
}

func (d *CZK) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if err := d.refreshTokenIfNeeded(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	// 根据API文档，下载链接接口需要添加Authorization认证头部
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
		log.Printf("CZK Link: failed to parse download link response: %v, response body: %s", err, string(resp.Body()))
		return nil, fmt.Errorf("failed to parse download link response: %w", err)
	}

	// 记录响应内容用于调试
	log.Printf("CZK Link response: %+v", downloadResp)

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

	// 根据API文档，响应可能为空对象，这种情况下我们记录警告但不报错
	if downloadLink == "" {
		log.Printf("CZK Link: warning - no download link found in response: %+v", downloadResp)
		return nil, fmt.Errorf("failed to get download link from response")
	}

	// 创建一个带有重试机制的链接
	return &model.Link{
		URL: downloadLink,
		Header: http.Header{
			"User-Agent": []string{"openlist"},
		},
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

	// 根据API文档，认证接口需要在请求头中包含x-api-key和x-api-secret
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
		log.Printf("CZK authenticate: failed to parse auth response: %v, response body: %s", err, string(resp.Body()))
		return fmt.Errorf("failed to parse auth response: %w, response body: %s", err, string(resp.Body()))
	}

	// 记录响应内容用于调试
	log.Printf("CZK authenticate response: Status=%d, Message=%s, Data.AccessToken=%s***, Data.RefreshToken=%s***, Data.ExpiresIn=%d, Data.TokenType=%s",
		authResp.Status, authResp.Message,
		authResp.Data.AccessToken[:min(len(authResp.Data.AccessToken), 10)],
		authResp.Data.RefreshToken[:min(len(authResp.Data.RefreshToken), 10)],
		authResp.Data.ExpiresIn, authResp.Data.TokenType)

	// 检查API返回的状态码
	// 根据经验，即使status不是200，但如果message是"认证成功"，我们也认为认证成功
	if authResp.Status != 200 && authResp.Message != "认证成功" {
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

	log.Printf("CZK authenticate: successfully authenticated, access token: %s***, refresh token: %s***, expires at: %v",
		d.AccessToken[:min(len(d.AccessToken), 10)], d.RefreshToken[:min(len(d.RefreshToken), 10)], d.ExpiresAt)

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

	log.Printf("CZK refreshToken: attempting to refresh token with refresh token: %s***", d.RefreshToken[:min(len(d.RefreshToken), 10)])

	// 创建表单数据，根据API文档，只需要refresh_token字段
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	_ = writer.WriteField("refresh_token", d.RefreshToken)
	err := writer.Close()
	if err != nil {
		return fmt.Errorf("failed to create refresh token form: %w", err)
	}

	// 设置请求超时时间
	d.client.SetTimeout(30 * time.Second)

	// 根据API文档，刷新令牌接口使用POST方法，请求体使用multipart/form-data格式
	resp, err := d.client.R().
		SetHeader("Content-Type", writer.FormDataContentType()).
		SetBody(payload.Bytes()).
		Post(url)

	if err != nil {
		return fmt.Errorf("failed to send refresh request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		log.Printf("CZK refreshToken: refresh request failed with status %d: %s, response body: %s", resp.StatusCode(), resp.Status(), string(resp.Body()))
		return fmt.Errorf("token refresh failed with status %d: %s, response body: %s", resp.StatusCode(), resp.Status(), string(resp.Body()))
	}

	// 解析刷新令牌响应，更新access_token等
	var refreshResp RefreshResp
	if err := json.Unmarshal(resp.Body(), &refreshResp); err != nil {
		log.Printf("CZK refreshToken: failed to parse refresh response: %v, response body: %s", err, string(resp.Body()))
		return fmt.Errorf("failed to parse refresh response: %w, response body: %s", err, string(resp.Body()))
	}

	// 记录响应内容用于调试
	log.Printf("CZK refreshToken response: Status=%d, Message=%s, Success=%t, Data.AccessToken=%s***, Data.ExpiresIn=%d, Data.TokenType=%s",
		refreshResp.Status, refreshResp.Message, refreshResp.Success,
		refreshResp.Data.AccessToken[:min(len(refreshResp.Data.AccessToken), 10)],
		refreshResp.Data.ExpiresIn, refreshResp.Data.TokenType)

	// 检查API返回的状态码和成功标志
	// 当Success为true且Status为200时，表示刷新成功
	if !refreshResp.Success || refreshResp.Status != 200 {
		// 特别处理"需要提供刷新令牌"和"无效或过期的刷新令牌"的错误
		if refreshResp.Message == "需要提供刷新令牌" || refreshResp.Message == "无效或过期的刷新令牌" {
			return fmt.Errorf("token refresh API error: status=%d, success=%t, message=%s, refresh token may be invalid or expired", refreshResp.Status, refreshResp.Success, refreshResp.Message)
		}
		return fmt.Errorf("token refresh API error: status=%d, success=%t, message=%s", refreshResp.Status, refreshResp.Success, refreshResp.Message)
	}

	// 更新访问令牌和过期时间
	d.AccessToken = refreshResp.Data.AccessToken
	d.ExpiresAt = time.Now().Add(time.Duration(refreshResp.Data.ExpiresIn) * time.Second)

	// 如果返回了新的刷新令牌，则更新它
	if refreshResp.Data.RefreshToken != "" {
		d.RefreshToken = refreshResp.Data.RefreshToken
		log.Printf("CZK refreshToken: new refresh token received and updated: %s***", d.RefreshToken[:min(len(d.RefreshToken), 10)])
	}

	log.Printf("CZK refreshToken: successfully refreshed token, access token: %s***, expires at: %v",
		d.AccessToken[:min(len(d.AccessToken), 10)], d.ExpiresAt)

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
	if code, ok := operationResp["code"].(float64); ok && int64(code) != 200 {
		message := "unknown error"
		if msg, ok := operationResp["msg"].(string); ok {
			message = msg
		} else if msg, ok := operationResp["message"].(string); ok {
			message = msg
		}
		return nil, fmt.Errorf("create folder API error: code=%d, message=%s", int64(code), message)
	}

	// 从响应中提取新创建的文件夹ID
	folderID := ""
	if data, ok := operationResp["data"].(map[string]interface{}); ok {
		if id, ok := data["folder_id"].(float64); ok {
			folderID = fmt.Sprintf("%.0f", id)
		}
	}

	// 返回新创建的目录对象
	newObj := &model.Object{
		ID:       folderID,
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
	// 根据API规范，目标目录ID使用target_id参数名
	_ = writer.WriteField("target_id", dstDir.GetID())
	err := writer.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to create move form: %w", err)
	}

	// 根据POST接口调用规范，需要在请求头中携带Authorization认证信息
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

	// 检查响应中是否有错误信息，根据API示例使用code字段
	if code, ok := operationResp["code"].(float64); ok && int64(code) != 200 {
		message := "unknown error"
		if msg, ok := operationResp["message"].(string); ok {
			message = msg
		} else if msg, ok := operationResp["msg"].(string); ok {
			// 根据示例响应，也可能使用msg字段
			message = msg
		}
		return nil, fmt.Errorf("move item API error: code=%d, message=%s", int64(code), message)
	}

	// 根据API示例响应格式解析返回的数据
	// 示例: {"code": 200, "msg": "成功", "data": {"items": [...]}}
	newObj := &model.Object{
		ID:       srcObj.GetID(),
		Name:     srcObj.GetName(),
		Size:     srcObj.GetSize(),
		Modified: time.Now(),
		IsFolder: srcObj.IsDir(),
	}

	// 从响应中提取更新后的对象信息
	if data, ok := operationResp["data"].(map[string]interface{}); ok {
		if items, ok := data["items"].([]interface{}); ok && len(items) > 0 {
			// 查找被移动的对象
			for _, itemData := range items {
				if itemMap, ok := itemData.(map[string]interface{}); ok {
					if id, ok := itemMap["id"].(float64); ok && fmt.Sprintf("%.0f", id) == srcObj.GetID() {
						// 找到被移动的对象，更新信息
						if name, ok := itemMap["name"].(string); ok {
							newObj.Name = name
						}

						// parentId 是新的父目录ID，但模型中没有直接存储这个信息
						// 我们只需要确保对象信息是最新的
						_ = itemMap["parent_id"]

						if createdAt, ok := itemMap["created_at"].(string); ok {
							if t, err := time.Parse("2006-01-02 15:04:05", createdAt); err == nil {
								newObj.Modified = t
							}
						}

						break
					}
				}
			}
		}
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

	// 检查响应中是否有错误信息，根据API示例使用code字段
	if code, ok := operationResp["code"].(float64); ok && int64(code) != 200 {
		message := "unknown error"
		if msg, ok := operationResp["msg"].(string); ok {
			// 根据API文档，使用msg字段而非message字段
			message = msg
		}
		return fmt.Errorf("delete item API error: code=%d, message=%s", int64(code), message)
	}

	return nil
}

func (d *CZK) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	if err := d.refreshTokenIfNeeded(); err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	// 增加请求超时时间以提高大文件上传的稳定性
	d.client.SetTimeout(10 * time.Minute)

	// 使用OpenList提供的工具函数计算文件的MD5哈希值
	tempFile, md5Hash, err := stream.CacheFullAndHash(file, &up, utils.MD5)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate file md5: %w", err)
	}

	// 重置文件流以便后续使用
	if seeker, ok := tempFile.(io.Seeker); ok {
		if _, err := seeker.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to seek file: %w", err)
		}
	}

	// 初始化上传
	initURL := "https://pan.szczk.top/czkapi/first_upload"

	// 创建表单数据
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	_ = writer.WriteField("hash", md5Hash)
	_ = writer.WriteField("filename", file.GetName())
	_ = writer.WriteField("filesize", fmt.Sprintf("%d", file.GetSize()))
	_ = writer.WriteField("folder", dstDir.GetID())
	err = writer.Close()
	if err != nil {
		return nil, fmt.Errorf("failed to create init upload form: %w", err)
	}

	resp, err := d.client.R().
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetHeader("Content-Type", writer.FormDataContentType()).
		SetBody(payload.Bytes()).
		Post(initURL)

	if err != nil {
		// 恢复默认超时时间
		d.client.SetTimeout(30 * time.Second)
		return nil, fmt.Errorf("failed to send init upload request: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		// 恢复默认超时时间
		d.client.SetTimeout(30 * time.Second)
		return nil, fmt.Errorf("failed to initialize upload with status %d: %s", resp.StatusCode(), resp.String())
	}

	// 解析初始化上传的响应
	var initResp map[string]interface{}
	if err := json.Unmarshal(resp.Body(), &initResp); err != nil {
		// 恢复默认超时时间
		d.client.SetTimeout(30 * time.Second)
		return nil, fmt.Errorf("failed to parse upload init response: %w", err)
	}

	// 检查响应中是否有错误信息，根据API示例使用code字段
	if code, ok := initResp["code"].(float64); ok && int64(code) != 200 {
		message := "unknown error"
		if msg, ok := initResp["msg"].(string); ok {
			// 根据API文档，使用msg字段而非message字段
			message = msg
		}
		// 恢复默认超时时间
		d.client.SetTimeout(30 * time.Second)
		return nil, fmt.Errorf("init upload API error: code=%d, message=%s", int64(code), message)
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
		// 恢复默认超时时间
		d.client.SetTimeout(30 * time.Second)
		return nil, fmt.Errorf("missing required parameters from init upload response: csrf_token=%s, file_key=%s", csrfToken, fileKey)
	}

	// 完成上传
	completeURL := "https://pan.szczk.top/czkapi/ok_upload"

	// 记录完成上传的参数用于调试
	log.Printf("CZK complete upload: url=%s, filename=%s, filesize=%d, folder=%s, csrf_token=%s***, file_key=%s***",
		completeURL, file.GetName(), file.GetSize(), dstDir.GetID(),
		csrfToken[:min(len(csrfToken), 10)], fileKey[:min(len(fileKey), 10)])

	// 创建完成上传的表单数据
	completePayload := &bytes.Buffer{}
	completeWriter := multipart.NewWriter(completePayload)
	_ = completeWriter.WriteField("hash", md5Hash)
	_ = completeWriter.WriteField("filename", file.GetName())
	_ = completeWriter.WriteField("filesize", fmt.Sprintf("%d", file.GetSize()))
	_ = completeWriter.WriteField("csrf_token", csrfToken)
	_ = completeWriter.WriteField("file_key", fileKey)
	_ = completeWriter.WriteField("folder", dstDir.GetID())
	err = completeWriter.Close()
	if err != nil {
		// 恢复默认超时时间
		d.client.SetTimeout(30 * time.Second)
		return nil, fmt.Errorf("failed to create complete upload form: %w", err)
	}

	completeResp, err := d.client.R().
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetHeader("Content-Type", completeWriter.FormDataContentType()).
		SetBody(completePayload.Bytes()).
		Post(completeURL)

	// 恢复默认超时时间
	d.client.SetTimeout(30 * time.Second)

	if err != nil {
		return nil, fmt.Errorf("failed to send complete upload request: %w", err)
	}

	// 记录API响应用于调试
	log.Printf("CZK complete upload response: status=%d, body=%s", completeResp.StatusCode(), string(completeResp.Body()))

	if completeResp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("failed to complete upload with status %d: %s", completeResp.StatusCode(), completeResp.String())
	}

	// 解析完成上传的响应
	var completeRespData map[string]interface{}
	if err := json.Unmarshal(completeResp.Body(), &completeRespData); err != nil {
		return nil, fmt.Errorf("failed to parse upload complete response: %w, response body: %s", err, string(completeResp.Body()))
	}

	// 检查响应中是否有错误信息，根据API示例使用code字段
	if code, ok := completeRespData["code"].(float64); ok && int64(code) != 200 {
		message := "unknown error"
		if msg, ok := completeRespData["msg"].(string); ok {
			// 根据API文档，使用msg字段而非message字段
			message = msg
		} else if msg, ok := completeRespData["message"].(string); ok {
			// 也检查message字段
			message = msg
		}

		// 提供更详细的错误信息，包括响应内容
		return nil, fmt.Errorf("complete upload API error: code=%d, message=%s, full response: %s", int64(code), message, string(completeResp.Body()))
	}

	// 记录成功响应
	log.Printf("CZK complete upload success: response=%+v", completeRespData)

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

// 添加min函数以避免编译错误
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
