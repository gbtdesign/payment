// Package handler 是 HTTP 接入层。
//
// 职责边界：解析与校验请求参数、把 dto 与 service 的入参出参互相转换、
// 按契约写响应。业务规则一律不写在这里——
// handler 里出现 if 判断订单状态或金额，就意味着业务逻辑漏到了接入层，
// 这部分逻辑将无法脱离 HTTP 被测试覆盖。
//
// 唯一的例外是渠道回调：微信要求按它自己的规范应答，
// 不走统一响应封装，详见 callback_handler.go。
package handler

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"

	"payment/internal/errcode"
	"payment/internal/pkg/money"
)

// maxParamDetailLen 限制回给客户端的参数错误详情长度。
const maxParamDetailLen = 512

// bindJSON 解析并校验 JSON 请求体。
//
// 把三类失败区分开是必要的，它们的 HTTP 语义与客户端处理方式完全不同：
//   - 请求体为空或 JSON 语法错误 -> ErrBadRequest，客户端要检查序列化
//   - 字段类型不匹配           -> ErrBadRequest，客户端要检查字段类型
//   - 校验规则不通过           -> ErrInvalidParam，客户端要提示用户改输入
//
// 统一返回「参数错误」会让前端无法给出有效的提示。
func bindJSON(c *gin.Context, dst any) error {
	err := c.ShouldBindJSON(dst)
	if err == nil {
		return nil
	}

	if errors.Is(err, io.EOF) {
		return errcode.ErrBadRequest.WithMsg("请求体为空")
	}

	var validationErrs validator.ValidationErrors
	if errors.As(err, &validationErrs) {
		detail := describeValidation(dst, validationErrs)
		return errcode.ErrInvalidParam.WithMsg(detail)
	}

	// 其余情况都是 JSON 解析层面的问题
	return errcode.ErrBadRequest.WithCause(err)
}

// describeValidation 把 validator 的错误翻译成可读的中文提示。
//
// 字段名取 JSON tag 而不是 Go 字段名：客户端只认自己发出去的 JSON 字段，
// 回一句 "Subject 为必填项" 会让前端对着 payload 找不到 Subject 这个键。
func describeValidation(dst any, errs validator.ValidationErrors) string {
	names := jsonFieldNames(dst)

	parts := make([]string, 0, len(errs))
	for _, fe := range errs {
		name := names[fe.Field()]
		if name == "" {
			name = fe.Field()
		}
		parts = append(parts, name+" "+ruleText(fe))
	}

	detail := "参数校验失败: " + strings.Join(parts, "; ")
	if len(detail) > maxParamDetailLen {
		detail = detail[:maxParamDetailLen] + "..."
	}
	return detail
}

// ruleText 描述单条未通过的校验规则。
func ruleText(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "为必填项"
	case "max":
		return fmt.Sprintf("长度不能超过 %s", fe.Param())
	case "min":
		return fmt.Sprintf("长度不能少于 %s", fe.Param())
	case "oneof":
		return fmt.Sprintf("取值必须是 [%s] 之一", fe.Param())
	case "gte":
		return fmt.Sprintf("不能小于 %s", fe.Param())
	case "lte":
		return fmt.Sprintf("不能大于 %s", fe.Param())
	default:
		return fmt.Sprintf("不满足校验规则 %s=%s", fe.Tag(), fe.Param())
	}
}

// jsonFieldNames 建立 Go 字段名到 JSON 字段名的映射。
func jsonFieldNames(dst any) map[string]string {
	names := make(map[string]string)

	t := reflect.TypeOf(dst)
	if t == nil {
		return names
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return names
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag, ok := field.Tag.Lookup("json")
		if !ok {
			continue
		}
		// tag 形如 "subject,omitempty"，只取逗号前的名字；"-" 表示不参与序列化
		name := strings.TrimSpace(strings.Split(tag, ",")[0])
		if name == "" || name == "-" {
			continue
		}
		names[field.Name] = name
	}
	return names
}

// pathParam 取出路径参数并校验非空。
func pathParam(c *gin.Context, key string) (string, error) {
	value := strings.TrimSpace(c.Param(key))
	if value == "" {
		return "", errcode.ErrInvalidParam.WithMsg("路径参数 " + key + " 不能为空")
	}
	return value, nil
}

// amountParseError 把 pkg/money 的解析错误翻译成面向客户端的提示。
//
// money 包的错误文本是写给开发者看的（如 "money: 金额小数位超过 2 位"），
// 直接回给客户端既暴露了内部包名，也没告诉用户应该怎么改。
func amountParseError(err error) error {
	switch {
	case errors.Is(err, money.ErrPrecisionLoss):
		return errcode.ErrOrderAmountInvalid.WithMsg("金额最多保留两位小数")
	case errors.Is(err, money.ErrInvalidFormat):
		return errcode.ErrOrderAmountInvalid.WithMsg("金额格式非法，应为形如 19.99 的字符串")
	case errors.Is(err, money.ErrOutOfRange):
		return errcode.ErrOrderAmountInvalid.WithMsg("金额超出允许范围")
	default:
		return errcode.ErrOrderAmountInvalid.WithCause(err)
	}
}
