package openai

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func OaiResponsesHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	// read response body
	var responsesResponse dto.OpenAIResponsesResponse
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeReadResponseBodyFailed, http.StatusInternalServerError)
	}
	err = common.Unmarshal(responseBody, &responsesResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oaiError := responsesResponse.GetOpenAIError(); oaiError != nil && oaiError.Type != "" {
		return nil, types.WithOpenAIError(*oaiError, resp.StatusCode)
	}

	info.ObserveResponseModel(responsesResponse.Model)
	responseBody = rewriteSGLangResponsesCreatedAt(info, responseBody, "created_at", responsesResponse.CreatedAt)

	// 写入新的 response body
	service.IOCopyBytesGracefully(c, resp, responseBody)

	// compute usage
	usage := &dto.Usage{}
	service.ApplyResponsesUsage(usage, responsesResponse.Usage)
	// Count actual tool invocations from Output (not tool declarations).
	for _, output := range responsesResponse.Output {
		switch output.Type {
		case dto.BuildInCallWebSearchCall:
			info.CountBillableToolCall(dto.BuildInCallWebSearchCall, "")
		case dto.BuildInCallFileSearchCall:
			info.CountBillableToolCall(dto.BuildInCallFileSearchCall, "")
		case dto.BuildInCallFunctionCall:
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, output.Name)
		}
	}
	info.ApplyVendorToolUsage(responseBody)

	imageCounter := &relaycommon.ImageGenerationCallCounter{}
	for i := range responsesResponse.Output {
		imageCounter.Observe(&responsesResponse.Output[i], &i)
	}
	imageCounter.Commit(info)

	return usage, nil
}

func OaiResponsesStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	if resp == nil || resp.Body == nil {
		logger.LogError(c, common.LogText("invalid response or response body"))
		return nil, types.NewError(fmt.Errorf("invalid response"), types.ErrorCodeBadResponse)
	}

	defer service.CloseResponseBodyGracefully(resp)

	accumulator := service.NewResponsesUsageAccumulator(info)

	// Hold lifecycle events back until the first visible output arrives so a
	// channel that fails before producing anything can still be retried.
	var (
		pendingEvents    []dto.ResponsesStreamResponse
		pendingEventData []string
		streamErr        *types.NewAPIError
		hasVisibleOutput bool
	)

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		if streamErr != nil {
			sr.Stop(streamErr)
			return
		}

		var streamResponse dto.ResponsesStreamResponse
		if err := common.UnmarshalJsonStr(data, &streamResponse); err != nil {
			streamErr = types.NewOpenAIError(fmt.Errorf("unmarshal responses stream event: %w", err), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
			sr.Stop(streamErr)
			return
		}
		if newAPIError := responsesStreamError(streamResponse); newAPIError != nil {
			if hasVisibleOutput {
				sendResponsesStreamData(c, streamResponse, data)
				types.ErrOptionWithSkipRetry()(newAPIError)
			}
			streamErr = newAPIError
			sr.Stop(streamErr)
			return
		}
		if streamResponse.Response != nil {
			data = string(rewriteSGLangResponsesCreatedAt(info, []byte(data), "response.created_at", streamResponse.Response.CreatedAt))
		}
		accumulator.Observe(&streamResponse, common.StringToByteSlice(data))

		if !hasVisibleOutput && isResponsesStreamLifecycleEvent(streamResponse.Type) {
			pendingEvents = append(pendingEvents, streamResponse)
			pendingEventData = append(pendingEventData, data)
			return
		}
		if !hasVisibleOutput {
			for index, pendingEvent := range pendingEvents {
				sendResponsesStreamData(c, pendingEvent, pendingEventData[index])
			}
			hasVisibleOutput = true
		}
		sendResponsesStreamData(c, streamResponse, data)
	})
	if streamErr != nil {
		return nil, streamErr
	}

	common.SetContextKey(c, constant.ContextKeyResponseStreamStatus, info.StreamStatus)
	info.StreamStatus.RequireTerminal()
	return accumulator.Finish(), nil
}

func rewriteSGLangResponsesCreatedAt(info *relaycommon.RelayInfo, payload []byte, path string, createdAt dto.IntValue) []byte {
	if info.GetChannelType() != constant.ChannelTypeSGLang {
		return payload
	}
	if !gjson.GetBytes(payload, path).Exists() {
		return payload
	}
	patched, err := sjson.SetBytes(payload, path, int(createdAt))
	if err != nil {
		return payload
	}
	return patched
}

func isResponsesStreamLifecycleEvent(eventType string) bool {
	switch eventType {
	case "response.created", "response.in_progress", "response.queued":
		return true
	default:
		return false
	}
}

// responsesStreamError reports an in-stream upstream error that is worth
// retrying on another channel. Only overload-class capacity errors are
// intercepted: business errors (4xx) and terminal response.failed events are
// forwarded to the client unchanged so upstream accounting stays intact.
func responsesStreamError(streamResponse dto.ResponsesStreamResponse) *types.NewAPIError {
	switch streamResponse.Type {
	case "error", "response.error":
	default:
		return nil
	}

	for _, openAIError := range responsesStreamErrorFields(streamResponse) {
		if openAIError.Message == "" || !isResponsesStreamOverloadError(openAIError) {
			continue
		}
		return types.WithOpenAIError(openAIError, http.StatusInternalServerError)
	}
	return nil
}

// responsesStreamErrorFields lists the error envelopes an upstream may use for
// a mid-stream failure, covering both the nested (error.error) shape and the
// flat (top-level code/message) shape used by the Responses SSE protocol.
func responsesStreamErrorFields(streamResponse dto.ResponsesStreamResponse) []types.OpenAIError {
	fields := make([]types.OpenAIError, 0, 3)
	if oaiErr := dto.GetOpenAIError(streamResponse.Error); oaiErr != nil {
		fields = append(fields, *oaiErr)
	}
	if streamResponse.Response != nil {
		if oaiErr := streamResponse.Response.GetOpenAIError(); oaiErr != nil {
			fields = append(fields, *oaiErr)
		}
	}
	if streamResponse.Message != "" || streamResponse.Code != "" {
		var code any
		if streamResponse.Code != "" {
			code = streamResponse.Code
		}
		fields = append(fields, types.OpenAIError{
			Message: streamResponse.Message,
			Type:    "upstream_error",
			Param:   streamResponse.Param,
			Code:    code,
		})
	}
	return fields
}

// responsesStreamOverloadErrors lists the upstream codes/types that mean the
// provider is temporarily out of capacity. Anything else (invalid_request,
// context_length_exceeded, server_error, ...) is a business error and is
// passed through to the client untouched.
var responsesStreamOverloadErrors = map[string]struct{}{
	"server_is_overloaded":      {},
	"server_overloaded":         {},
	"overloaded":                {},
	"overloaded_error":          {},
	"service_unavailable":       {},
	"service_unavailable_error": {},
	"capacity_exceeded":         {},
}

func isResponsesStreamOverloadError(openAIError types.OpenAIError) bool {
	for _, value := range []string{common.Interface2String(openAIError.Code), openAIError.Type} {
		if _, exists := responsesStreamOverloadErrors[strings.ToLower(strings.TrimSpace(value))]; exists {
			return true
		}
	}
	return false
}
