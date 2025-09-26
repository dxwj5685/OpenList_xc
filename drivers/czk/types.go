package czk

import "time"

// AuthResp 认证响应结构
type AuthResp struct {
	Data struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
	} `json:"data"`
	Message string `json:"message"`
	Status  int64  `json:"status"`
}

// RefreshResp 刷新令牌响应结构
type RefreshResp struct {
	Data struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		TokenType   string `json:"token_type"`
	} `json:"data"`
	FileID  string `json:"file_id,omitempty"`
	Message string `json:"message"`
	Status  int64  `json:"status"`
	Success bool   `json:"success,omitempty"`
}

// OperationResp 通用操作响应结构（用于重命名、删除、移动等操作）
type OperationResp struct {
	Status  int                    `json:"status"`
	Message string                 `json:"message"`
	Data    map[string]interface{} `json:"data,omitempty"`
}

// File 文件信息结构
type File struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"modified"`
	IsFolder bool   `json:"is_folder"`
}