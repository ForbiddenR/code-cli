package anthropicapi

import (
	"encoding/json"
	"fmt"

	"code-cli/internal/core"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

func newMessageParams(req MessageRequest) (anthropic.MessageNewParams, error) {
	req = req.WithDefaults()

	messages, err := messageParams(req.Messages)
	if err != nil {
		return anthropic.MessageNewParams{}, err
	}
	system := systemParams(req.System)
	tools, err := messageTools(req.Tools, req.ServerTools)
	if err != nil {
		return anthropic.MessageNewParams{}, err
	}

	params := anthropic.MessageNewParams{
		MaxTokens:     int64(req.MaxTokens),
		Messages:      messages,
		Model:         anthropic.Model(req.Model.String()),
		StopSequences: append([]string(nil), req.StopSequences...),
		System:        system,
		Tools:         tools,
	}
	if req.ToolChoice != nil {
		choice, err := toolChoiceParam(*req.ToolChoice)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		params.ToolChoice = choice
	}
	if len(req.Metadata) > 0 {
		metadata, err := metadataParam(req.Metadata)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		params.Metadata = metadata
	}
	if req.Thinking != nil {
		thinking, err := thinkingParam(*req.Thinking)
		if err != nil {
			return anthropic.MessageNewParams{}, err
		}
		params.Thinking = thinking
	}
	if req.OutputConfig != nil {
		params.OutputConfig = outputConfigParam(*req.OutputConfig)
	}

	return params, nil
}

func newTokenCountParams(req TokenCountRequest) (anthropic.MessageCountTokensParams, error) {
	if req.Model == "" {
		req.Model = core.DefaultModel
	}

	messages, err := messageParams(req.Messages)
	if err != nil {
		return anthropic.MessageCountTokensParams{}, err
	}
	system := systemParams(req.System)
	tools, err := tokenCountToolParams(req.Tools)
	if err != nil {
		return anthropic.MessageCountTokensParams{}, err
	}

	params := anthropic.MessageCountTokensParams{
		Messages: messages,
		Model:    anthropic.Model(req.Model.String()),
		System: anthropic.MessageCountTokensParamsSystemUnion{
			OfTextBlockArray: system,
		},
		Tools: tools,
	}
	if req.Thinking != nil {
		thinking, err := thinkingParam(*req.Thinking)
		if err != nil {
			return anthropic.MessageCountTokensParams{}, err
		}
		params.Thinking = thinking
	}
	if req.OutputConfig != nil {
		params.OutputConfig = outputConfigParam(*req.OutputConfig)
	}

	return params, nil
}

func messageParams(messages []core.Message) ([]anthropic.MessageParam, error) {
	params := make([]anthropic.MessageParam, 0, len(messages))
	for i, message := range messages {
		content, err := contentBlockParams(message.Content)
		if err != nil {
			return nil, fmt.Errorf("convert message %d content: %w", i, err)
		}

		role, err := messageRole(message.Role)
		if err != nil {
			return nil, fmt.Errorf("convert message %d role: %w", i, err)
		}

		params = append(params, anthropic.MessageParam{
			Role:    role,
			Content: content,
		})
	}
	return params, nil
}

func messageRole(role core.Role) (anthropic.MessageParamRole, error) {
	switch role {
	case core.RoleUser:
		return anthropic.MessageParamRoleUser, nil
	case core.RoleAssistant:
		return anthropic.MessageParamRoleAssistant, nil
	default:
		return "", fmt.Errorf("unsupported role %q", role)
	}
}

func contentBlockParams(blocks []core.ContentBlock) ([]anthropic.ContentBlockParamUnion, error) {
	params := make([]anthropic.ContentBlockParamUnion, 0, len(blocks))
	for i, block := range blocks {
		param, err := contentBlockParam(block)
		if err != nil {
			return nil, fmt.Errorf("convert content block %d: %w", i, err)
		}
		params = append(params, param)
	}
	return params, nil
}

func contentBlockParam(block core.ContentBlock) (anthropic.ContentBlockParamUnion, error) {
	data, err := json.Marshal(block)
	if err != nil {
		return anthropic.ContentBlockParamUnion{}, err
	}
	return param.Override[anthropic.ContentBlockParamUnion](json.RawMessage(data)), nil
}

func systemParams(blocks []core.SystemBlock) []anthropic.TextBlockParam {
	params := make([]anthropic.TextBlockParam, 0, len(blocks))
	for _, block := range blocks {
		text := anthropic.TextBlockParam{Text: block.Text}
		if block.CacheControl != nil {
			text.CacheControl = anthropic.NewCacheControlEphemeralParam()
		}
		params = append(params, text)
	}
	return params
}

func toolParams(tools []core.ToolDefinition) ([]anthropic.ToolUnionParam, error) {
	params := make([]anthropic.ToolUnionParam, 0, len(tools))
	for i, tool := range tools {
		toolParam, err := sdkToolParam(tool)
		if err != nil {
			return nil, fmt.Errorf("convert tool %d: %w", i, err)
		}
		params = append(params, anthropic.ToolUnionParam{OfTool: &toolParam})
	}
	return params, nil
}

func messageTools(custom []core.ToolDefinition, server []ServerToolDefinition) ([]anthropic.ToolUnionParam, error) {
	tools, err := toolParams(custom)
	if err != nil {
		return nil, err
	}
	serverTools, err := serverToolParams(server)
	if err != nil {
		return nil, err
	}
	return append(tools, serverTools...), nil
}

func serverToolParams(tools []ServerToolDefinition) ([]anthropic.ToolUnionParam, error) {
	params := make([]anthropic.ToolUnionParam, 0, len(tools))
	for i, tool := range tools {
		toolParam, err := serverToolParam(tool)
		if err != nil {
			return nil, fmt.Errorf("convert server tool %d: %w", i, err)
		}
		params = append(params, toolParam)
	}
	return params, nil
}

func serverToolParam(tool ServerToolDefinition) (anthropic.ToolUnionParam, error) {
	if tool.Type != ServerToolWebSearch20250305 {
		return anthropic.ToolUnionParam{}, fmt.Errorf("unsupported server tool type %q", tool.Type)
	}
	if tool.Name == "" {
		tool.Name = "web_search"
	}
	if tool.Name != "web_search" {
		return anthropic.ToolUnionParam{}, fmt.Errorf("unsupported web-search tool name %q", tool.Name)
	}

	variant := anthropic.WebSearchTool20250305Param{
		AllowedDomains: append([]string(nil), tool.AllowedDomains...),
		BlockedDomains: append([]string(nil), tool.BlockedDomains...),
		Name:           constant.WebSearch(tool.Name),
		Type:           constant.WebSearch20250305(tool.Type),
	}
	if tool.MaxUses > 0 {
		variant.MaxUses = param.NewOpt(int64(tool.MaxUses))
	}
	return anthropic.ToolUnionParam{OfWebSearchTool20250305: &variant}, nil
}

func toolChoiceParam(choice ToolChoice) (anthropic.ToolChoiceUnionParam, error) {
	switch choice.Type {
	case "tool":
		if choice.Name == "" {
			return anthropic.ToolChoiceUnionParam{}, fmt.Errorf("tool choice name is required")
		}
		return anthropic.ToolChoiceParamOfTool(choice.Name), nil
	default:
		return anthropic.ToolChoiceUnionParam{}, fmt.Errorf("unsupported tool choice type %q", choice.Type)
	}
}

func tokenCountToolParams(tools []core.ToolDefinition) ([]anthropic.MessageCountTokensToolUnionParam, error) {
	params := make([]anthropic.MessageCountTokensToolUnionParam, 0, len(tools))
	for i, tool := range tools {
		toolParam, err := sdkToolParam(tool)
		if err != nil {
			return nil, fmt.Errorf("convert token-count tool %d: %w", i, err)
		}
		params = append(params, anthropic.MessageCountTokensToolUnionParam{OfTool: &toolParam})
	}
	return params, nil
}

func sdkToolParam(tool core.ToolDefinition) (anthropic.ToolParam, error) {
	schema := tool.InputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	if !json.Valid(schema) {
		return anthropic.ToolParam{}, fmt.Errorf("invalid JSON schema for tool %q", tool.Name)
	}

	toolParam := anthropic.ToolParam{
		InputSchema: param.Override[anthropic.ToolInputSchemaParam](schema),
		Name:        tool.Name,
		Type:        anthropic.ToolTypeCustom,
	}
	if tool.Description != "" {
		toolParam.Description = param.NewOpt(tool.Description)
	}
	return toolParam, nil
}

func metadataParam(metadata map[string]string) (anthropic.MetadataParam, error) {
	data, err := json.Marshal(metadata)
	if err != nil {
		return anthropic.MetadataParam{}, err
	}
	return param.Override[anthropic.MetadataParam](json.RawMessage(data)), nil
}

func thinkingParam(thinking core.ThinkingConfig) (anthropic.ThinkingConfigParamUnion, error) {
	data, err := json.Marshal(thinking)
	if err != nil {
		return anthropic.ThinkingConfigParamUnion{}, err
	}
	return param.Override[anthropic.ThinkingConfigParamUnion](json.RawMessage(data)), nil
}

func outputConfigParam(config core.OutputConfig) anthropic.OutputConfigParam {
	return anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffort(config.Effort)}
}
