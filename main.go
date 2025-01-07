package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/joho/godotenv"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// ChatCompletionRequest はOpenAIのチャット補完リクエストの構造体です
type ChatCompletionRequest struct {
	Model    string                  `json:"model"`
	Messages []ChatCompletionMessage `json:"messages"`
	Stream   bool                    `json:"stream"`
}

// ChatCompletionMessage はメッセージ構造体です
type ChatCompletionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionResponse はOpenAIのチャット補完レスポンスの構造体です
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int                    `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   Usage                  `json:"usage"`
}

// ChatCompletionChoice は選択肢の構造体です
type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Delta        Delta       `json:"delta"`
	FinishReason interface{} `json:"finish_reason"`
}

// Delta はデルタデータの構造体です
type Delta struct {
	Content string `json:"content"`
}

// Usage は使用状況の構造体です
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
type OpenAIClient struct {
	APIKey     string
	HTTPClient *http.Client
	Endpoint   string
}

// NewOpenAIClient は新しいOpenAIClientを作成します
func NewOpenAIClient(apiKey string) *OpenAIClient {
	return &OpenAIClient{
		APIKey: apiKey,
		HTTPClient: &http.Client{
			Timeout: time.Minute * 10,
		},
		Endpoint: "https://api.openai.com/v1/chat/completions",
	}
}

// StreamUsecase はビジネスロジックを含む構造体
type StreamUsecase struct {
	OpenAIClient *OpenAIClient
}

// ExecuteQuestion は質問を処理し、回答をストリーミングします
func (u *StreamUsecase) ExecuteQuestion(ctx context.Context, question string, streamCh chan<- string) error {
	request := ChatCompletionRequest{
		Model: "gpt-3.5-turbo",
		Messages: []ChatCompletionMessage{
			{
				Role:    "user",
				Content: question,
			},
		},
		Stream: true,
	}

	return u.OpenAIClient.SendChatCompletionRequest(ctx, request, streamCh)
}

// SendChatCompletionRequest はチャット補完リクエストを送信します
func (client *OpenAIClient) SendChatCompletionRequest(ctx context.Context, request ChatCompletionRequest, streamCh chan<- string) error {
	defer close(streamCh)

	// リクエストボディをJSONにエンコード
	jsonData, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("リクエストのJSONエンコードエラー: %v", err)
	}

	// HTTPリクエストを作成
	req, err := http.NewRequestWithContext(ctx, "POST", client.Endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("HTTPリクエスト作成エラー: %v", err)
	}

	// 必要なヘッダーを設定
	req.Header.Set("Authorization", "Bearer "+client.APIKey)
	req.Header.Set("Content-Type", "application/json")

	// リクエストを送信
	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTPリクエスト送信エラー: %v", err)
	}
	defer resp.Body.Close()

	// ストリーミングレスポンスのステータスコードをチェック
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("OpenAI APIエラー: %s", string(bodyBytes))
	}

	// レスポンスボディを読み取り
	reader := bufio.NewReader(resp.Body)

	for {
		// 一行ずつ読み取る
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("レスポンス読み取りエラー: %v", err)
		}

		line = strings.TrimSpace(line)

		// データの前置きを取り除く
		if strings.HasPrefix(line, "data: ") {
			line = strings.TrimPrefix(line, "data: ")

			if line == "[DONE]" {
				break
			}

			// JSONをパース
			var completionResp ChatCompletionResponse
			err := json.Unmarshal([]byte(line), &completionResp)
			if err != nil {
				return fmt.Errorf("レスポンスJSONパースエラー: %v", err)
			}

			// 各チャンクを処理
			for _, choice := range completionResp.Choices {
				if choice.Delta.Content != "" {
					streamCh <- choice.Delta.Content
				}
			}
		}
	}

	return nil
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("環境変数 OPENAI_API_KEY が設定されていません")
	}

	// OpenAIクライアントを初期化
	openAIClient := NewOpenAIClient(apiKey)

	// Usecaseを初期化
	usecase := &StreamUsecase{
		OpenAIClient: openAIClient,
	}

	// Echoインスタンスを作成
	e := echo.New()

	// ミドルウェアを設定
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	e.POST("/api", func(c echo.Context) error {
		// JSONリクエストから質問を取得
		type requestPayload struct {
			Prompt string `json:"prompt"`
		}

		var payload requestPayload
		if err := c.Bind(&payload); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Invalid request payload"})
		}

		inputPrompt := strings.TrimSpace(payload.Prompt)
		if inputPrompt == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "Prompt cannot be empty"})
		}

		// ストリーミング用チャネルを作成
		streamCh := make(chan string, 10000)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// 非同期で質問を処理
		go func() {
			if err := usecase.ExecuteQuestion(ctx, inputPrompt, streamCh); err != nil {
				log.Printf("Error: %v\n", err)
				cancel()
			}
		}()

		// ストリーミングレスポンスを開始
		c.Response().Header().Set(echo.HeaderContentType, "text/event-stream")
		c.Response().Header().Set("Cache-Control", "no-cache")
		c.Response().Header().Set("Connection", "keep-alive")
		c.Response().WriteHeader(http.StatusOK)

		// チャンクを逐次送信
		for {
			select {
			case <-ctx.Done():
				return nil // 処理が中断された場合は終了
			case chunk, ok := <-streamCh:
				if !ok {
					// チャネルが閉じた場合、ストリームの終了を通知
					_, err := c.Response().Write([]byte("data: { \"data\": \"[DONE]\" }\n\n"))
					if err != nil {
						log.Printf("Error writing DONE: %v\n", err)
					}
					return nil
				}

				// チャンクをJSON形式に整形して送信
				formattedChunk := fmt.Sprintf("data: { \"data\": \"%s\" }\n\n", chunk)
				_, err := c.Response().Write([]byte(formattedChunk))
				if err != nil {
					log.Printf("Error writing chunk: %v\n", err)
					return nil
				}

				// ストリームをフラッシュして即時反映
				c.Response().Flush()
			}
		}
	})

	// サーバーを起動
	e.Logger.Fatal(e.Start(":8080"))
}
