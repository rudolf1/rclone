// Package telegram implements a minimal skeleton for a new rclone backend called telegram.
// This is just a starting point for further development.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
)

// TelegramConfig holds configuration for the Telegram backend
// (token, chat_id)
type TelegramConfig struct {
	BotToken string
	ChatID   string
}

// Fs represents a remote telegram filesystem
// Implements fs.Fs

type Fs struct {
	name   string
	root   string
	config TelegramConfig
}

func init() {
	fs.Register(&fs.RegInfo{
		Name:        "telegram",
		Description: "Telegram Cloud Storage (example)",
		NewFs:       NewFs,
	})
}

// NewFs constructs a new Fs object
func NewFs(name, root string, m configmap.Mapper) (fs.Fs, error) {
	botToken := os.Getenv("RCLONE_TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("RCLONE_TELEGRAM_CHAT_ID")
	if botToken == "" || chatID == "" {
		return nil, fmt.Errorf("RCLONE_TELEGRAM_BOT_TOKEN and RCLONE_TELEGRAM_CHAT_ID must be set in environment")
	}
	cfg := TelegramConfig{BotToken: botToken, ChatID: chatID}
	return &Fs{name: name, root: root, config: cfg}, nil
}

// Put uploads an object to telegram
func (f *Fs) Put(ctx context.Context, in fs.ObjectInfo, src fs.ReaderAtSeeker, options ...fs.OpenOption) (fs.Object, error) {
	// Read all data from src
	buf := new(bytes.Buffer)
	_, err := io.Copy(buf, src)
	if err != nil {
		return nil, err
	}

	// Prepare multipart form data
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("document", in.Remote())
	if err != nil {
		return nil, err
	}
	_, err = part.Write(buf.Bytes())
	if err != nil {
		return nil, err
	}
	writer.WriteField("chat_id", f.config.ChatID)
	writer.Close()

	// Send file to Telegram
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", f.config.BotToken)
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram upload failed: %s", resp.Status)
	}

	// --- Save file info to file list and upload as JSON ---
	fileList, err := f.loadFileList(ctx)
	if err != nil {
		fileList = []string{} // если файла нет, начинаем с пустого списка
	}
	fileList = append(fileList, in.Remote())
	jsonData, err := json.MarshalIndent(fileList, "", "  ")
	if err != nil {
		return nil, err
	}
	// Отправляем файл filelist.json в чат
	jsonBody := &bytes.Buffer{}
	jsonWriter := multipart.NewWriter(jsonBody)
	jsonPart, err := jsonWriter.CreateFormFile("document", "filelist.json")
	if err != nil {
		return nil, err
	}
	_, err = jsonPart.Write(jsonData)
	if err != nil {
		return nil, err
	}
	jsonWriter.WriteField("chat_id", f.config.ChatID)
	jsonWriter.Close()
	jsonReq, err := http.NewRequestWithContext(ctx, "POST", url, jsonBody)
	if err != nil {
		return nil, err
	}
	jsonReq.Header.Set("Content-Type", jsonWriter.FormDataContentType())
	jsonResp, err := http.DefaultClient.Do(jsonReq)
	if err != nil {
		return nil, err
	}
	defer jsonResp.Body.Close()
	if jsonResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram filelist upload failed: %s", jsonResp.Status)
	}

	return nil, nil // TODO: return a valid fs.Object implementation
}

// loadFileList загружает список файлов из последнего filelist.json в чате
func (f *Fs) loadFileList(ctx context.Context) ([]string, error) {
	// Получаем последние сообщения чата
	url := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", f.config.BotToken)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var updates struct {
		Result []struct {
			Message struct {
				Document *struct {
					FileName string `json:"file_name"`
					FileID   string `json:"file_id"`
				} `json:"document"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&updates); err != nil {
		return nil, err
	}
	var fileID string
	for i := len(updates.Result) - 1; i >= 0; i-- {
		msg := updates.Result[i].Message
		if msg.Document != nil && msg.Document.FileName == "filelist.json" {
			fileID = msg.Document.FileID
			break
		}
	}
	if fileID == "" {
		return nil, fmt.Errorf("filelist.json not found")
	}
	// Получаем файл по file_id
	fileInfoURL := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", f.config.BotToken, fileID)
	fileInfoResp, err := http.Get(fileInfoURL)
	if err != nil {
		return nil, err
	}
	defer fileInfoResp.Body.Close()
	var fileInfo struct {
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(fileInfoResp.Body).Decode(&fileInfo); err != nil {
		return nil, err
	}
	fileDownloadURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", f.config.BotToken, fileInfo.Result.FilePath)
	fileResp, err := http.Get(fileDownloadURL)
	if err != nil {
		return nil, err
	}
	defer fileResp.Body.Close()
	var fileList []string
	if err := json.NewDecoder(fileResp.Body).Decode(&fileList); err != nil {
		return nil, err
	}
	return fileList, nil
}

// List the objects and directories in dir into entries
func (f *Fs) List(ctx context.Context, dir string) (entries fs.DirEntries, err error) {
	fileList, err := f.loadFileList(ctx)
	if err != nil {
		return nil, err
	}
	entries = fs.DirEntries{}
	for _, name := range fileList {
		obj := &TelegramObject{
			fs:   f,
			name: name,
		}
		entries = append(entries, obj)
	}
	return entries, nil
}

// TelegramObject реализует fs.Object для файлов Telegram
// (минимальная заглушка для List)
type TelegramObject struct {
	fs   *Fs
	name string
}

func (o *TelegramObject) Remote() string { return o.name }
func (o *TelegramObject) ModTime(ctx context.Context) (t fs.Time, err error) { return fs.Time{}, nil }
func (o *TelegramObject) Size() int64 { return 0 }
func (o *TelegramObject) Fs() fs.Info { return o.fs }
func (o *TelegramObject) String() string { return o.name }
func (o *TelegramObject) Storable() bool { return true }
func (o *TelegramObject) Hash(ctx context.Context, ty fs.HashType) (string, error) { return "", fs.ErrorHashUnsupported }
func (o *TelegramObject) Remove(ctx context.Context) error { return fs.ErrorNotImplemented }
func (o *TelegramObject) Update(ctx context.Context, in fs.ObjectInfo, src io.Reader, options ...fs.OpenOption) error { return fs.ErrorNotImplemented }
func (o *TelegramObject) Open(ctx context.Context, options ...fs.OpenOption) (io.ReadCloser, error) { return nil, fs.ErrorNotImplemented }
