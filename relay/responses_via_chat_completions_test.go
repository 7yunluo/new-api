package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResponsesViaChatCompletionsForwardsConvertedRequestAndReturnsResponses(t *testing.T) {
	service.InitHttpClient()
	upstreamRequests := make(chan struct {
		path    string
		request dto.GeneralOpenAIRequest
		err     error
	}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var forwarded dto.GeneralOpenAIRequest
		err := common.DecodeJson(request.Body, &forwarded)
		upstreamRequests <- struct {
			path    string
			request dto.GeneralOpenAIRequest
			err     error
		}{path: request.URL.Path, request: forwarded, err: err}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","created":1700000000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"晴天"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/responses", strings.NewReader(`{}`))
	context.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeResponses,
		RelayFormat:     types.RelayFormatOpenAIResponses,
		OriginModelName: "gpt-test",
		RequestURLPath:  "/v1/responses",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			ApiType:           constant.APITypeOpenAI,
			ChannelBaseUrl:    upstream.URL,
			ApiKey:            "test-key",
			UpstreamModelName: "gpt-test",
		},
	}
	adaptor := &openai.Adaptor{}
	adaptor.Init(info)

	usage, apiErr := responsesViaChatCompletions(context, info, adaptor, &dto.OpenAIResponsesRequest{
		Model:           "gpt-test",
		Input:           json.RawMessage(`"天气如何"`),
		Temperature:     common.GetPointer(0.2),
		MaxOutputTokens: common.GetPointer(uint(128)),
	})

	require.Nil(t, apiErr)
	require.Equal(t, 12, usage.TotalTokens)
	forwarded := <-upstreamRequests
	require.NoError(t, forwarded.err)
	require.Equal(t, "/v1/chat/completions", forwarded.path)
	require.Equal(t, "gpt-test", forwarded.request.Model)
	require.Len(t, forwarded.request.Messages, 1)
	require.Equal(t, "user", forwarded.request.Messages[0].Role)
	require.Equal(t, "天气如何", forwarded.request.Messages[0].StringContent())
	require.Equal(t, 0.2, *forwarded.request.Temperature)
	require.Equal(t, uint(128), *forwarded.request.MaxCompletionTokens)

	var response dto.OpenAIResponsesResponse
	require.NoError(t, common.DecodeJson(recorder.Body, &response))
	require.JSONEq(t, `"completed"`, string(response.Status))
	require.Len(t, response.Output, 1)
	require.Equal(t, "message", response.Output[0].Type)
	require.Equal(t, "assistant", response.Output[0].Role)
	require.Equal(t, "晴天", response.Output[0].Content[0].Text)
	require.Equal(t, 12, response.Usage.TotalTokens)
}

func TestResponsesViaChatCompletionsConvertsChatStreamToResponsesEvents(t *testing.T) {
	service.InitHttpClient()
	constant.StreamingTimeout = 300
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		stream := `data: {"id":"chatcmpl_2","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"晴"},"finish_reason":null}]}` + "\n\n" +
			`data: {"id":"chatcmpl_2","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test","choices":[{"index":0,"delta":{"content":"天"},"finish_reason":null}]}` + "\n\n" +
			`data: {"id":"chatcmpl_2","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
			`data: {"id":"chatcmpl_2","object":"chat.completion.chunk","created":1700000000,"model":"gpt-test","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}` + "\n\n" +
			`data: [DONE]` + "\n\n"
		_, _ = writer.Write([]byte(stream))
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/responses?stream=true", strings.NewReader(`{}`))
	context.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		RelayMode:          relayconstant.RelayModeResponses,
		RelayFormat:        types.RelayFormatOpenAIResponses,
		OriginModelName:    "gpt-test",
		RequestURLPath:     "/v1/responses",
		IsStream:           true,
		ShouldIncludeUsage: true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			ApiType:           constant.APITypeOpenAI,
			ChannelBaseUrl:    upstream.URL,
			ApiKey:            "test-key",
			UpstreamModelName: "gpt-test",
		},
	}
	adaptor := &openai.Adaptor{}
	adaptor.Init(info)

	usage, apiErr := responsesViaChatCompletions(context, info, adaptor, &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`"天气如何"`),
	})

	require.Nil(t, apiErr)
	require.Equal(t, 7, usage.TotalTokens)
	body := recorder.Body.String()
	require.True(t, strings.Contains(body, "response.output_item.added"), "missing output_item.added event: %s", body)
	require.True(t, strings.Contains(body, "response.output_text.delta"), "missing output_text.delta event: %s", body)
	require.True(t, strings.Contains(body, "晴"), "missing text delta content: %s", body)
	require.True(t, strings.Contains(body, "response.completed"), "missing completed event: %s", body)
	require.True(t, strings.Contains(body, `"total_tokens":7`), "missing usage in completed event: %s", body)
}

func TestResponsesHelperRejectsCompactWhenResponsesViaChatCompletionsEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, "http://gateway.test/v1/responses/compact", strings.NewReader(`{}`))
	context.Request.Header.Set("Content-Type", "application/json")
	info := &relaycommon.RelayInfo{
		RelayMode:       relayconstant.RelayModeResponsesCompact,
		RelayFormat:     types.RelayFormatOpenAIResponses,
		OriginModelName: "gpt-test",
		RequestURLPath:  "/v1/responses/compact",
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType: constant.ChannelTypeOpenAI,
			ApiType:     constant.APITypeOpenAI,
			ChannelOtherSettings: dto.ChannelOtherSettings{
				ResponsesViaChatCompletions: true,
			},
			UpstreamModelName: "gpt-test",
		},
	}
	adaptor := &openai.Adaptor{}
	adaptor.Init(info)

	usage, apiErr := responsesViaChatCompletions(context, info, adaptor, &dto.OpenAIResponsesRequest{
		Model: "gpt-test",
		Input: json.RawMessage(`"天气如何"`),
	})

	require.Nil(t, usage)
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
	require.Contains(t, apiErr.Error(), "responses/compact")
}
