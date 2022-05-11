package easygin

import (
	"sync"
	"sync/atomic"

	"time"

	_ "github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/lvhuat/code"
	"github.com/sirupsen/logrus"
)

type Response struct {
	Result    bool        `json:"result"`
	Mcode     string      `json:"mcode,omitempty"`
	Message   string      `json:"message,omitempty"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp int64       `json:"timestamp"`
}

type NopResponse interface {
	NopResponse()
}

func NewErrorResponse(code code.Error) *Response {
	return &Response{
		Mcode:     code.Mcode(),
		Timestamp: time.Now().UnixNano() / int64(time.Millisecond),
		Message:   code.Message(),
	}
}

func NewOkResponse(data interface{}) *Response {
	return &Response{
		Result:    true,
		Timestamp: time.Now().UnixNano() / int64(time.Millisecond),
		Data:      data,
	}
}

type StatusError interface {
	HttpStatus() int
}

type Wrapper struct {
	pool sync.Pool
	cfg  *Config
}

func New(cfg *Config) *Wrapper {
	w := &Wrapper{
		pool: sync.Pool{
			New: func() interface{} {
				return new(Response)
			},
		},
		cfg: cfg,
	}

	w.cfg.GinEngine.Use(w.LogNotProcess())

	return w
}

var (
	ErrExceedMaxPendingRequest = code.NewMcodef("EXCEED_MAX_PENDING_REQUEST", "exceed max pending request")
)

type WrappedFunc func(ctx *gin.Context) (interface{}, error)

type HttpServer interface {
	Handle(string, string, ...gin.HandlerFunc) gin.IRoutes
}

type Config struct {
	GinEngine *gin.Engine
	WrapOption
}

const (
	LogTypeOff = iota * 0x1000
	LogTypeSuccess
	LogTypeError
)

type WrapOption struct {
	log                *logrus.Entry
	maxPendingRequests *int
	defaultErrorStatus *int
	logMode            *int
	onRecover          func(interface{}) error
	requestWeight      int
	convertError       func(err error) code.Error
}

func NewWrapOption() *WrapOption {
	return &WrapOption{}
}

func (opt *WrapOption) LogEntry(log *logrus.Entry) *WrapOption {
	opt.log = log
	return opt
}

func (opt *WrapOption) MaxPendingRequests(limit int) *WrapOption {
	opt.maxPendingRequests = &limit
	return opt
}

func (opt *WrapOption) ErrorStatus(status int) *WrapOption {
	opt.defaultErrorStatus = &status
	return opt
}

func (opt *WrapOption) LogMode(mode int) *WrapOption {
	opt.logMode = &mode
	return opt
}

func (opt *WrapOption) OnRecoverError(fn func(interface{}) error) *WrapOption {
	opt.onRecover = fn
	return opt
}

func (opt *WrapOption) ConvertError(fn func(error) code.Error) *WrapOption {
	opt.convertError = fn
	return opt
}

func (opt *WrapOption) Merge(from *WrapOption) *WrapOption {
	if from.logMode != nil {
		opt.logMode = from.logMode
	}

	if from.onRecover != nil {
		opt.onRecover = from.onRecover
	}

	if from.maxPendingRequests != nil {
		opt.maxPendingRequests = from.maxPendingRequests
	}

	if from.log != nil {
		opt.log = nil
	}

	return opt
}

// Wrap 为gin的回调接口增加了固定的返回值，当程序收到处理结果的时候会将返回值封装一层再发送到网络, registPath为注册路径
func (wrapper *Wrapper) Wrap(f WrappedFunc, regPath string, options ...*WrapOption) gin.HandlerFunc {
	opt := wrapper.cfg.WrapOption // copy

	for _, option := range options {
		opt.Merge(option)
	}

	recoverFunc := opt.onRecover
	log := *opt.log
	maxPendingRequest := *opt.maxPendingRequests
	requestWeight := 1
	if opt.requestWeight != 0 {
		requestWeight = opt.requestWeight
	}

	logMode := LogTypeError | LogTypeSuccess
	if opt.logMode != nil {
		logMode = *opt.logMode
	}

	onError := opt.convertError
	pendingRequests := int64(0)
	defaultErrorStatus := 200
	if opt.defaultErrorStatus != nil {
		defaultErrorStatus = *opt.defaultErrorStatus
	}

	return func(httpCtx *gin.Context) {
		if httpCtx.Keys == nil {
			httpCtx.Keys = make(map[string]interface{}, 1)
		}

		httpCtx.Keys["easygin"] = 1

		since := time.Now()
		var (
			data    interface{}
			err     error
			retErr  code.Error
			pending int64
		)

		defer func() {
			// 拦截业务层的异常
			if r := recover(); r != nil {
				if recoverFunc != nil {
					r = recoverFunc(r)
				}

				if codeErr, ok := r.(code.Error); ok {
					retErr = codeErr
				} else {
					retErr = code.NewMcode("INTERNAL_ERROR", "Service internal error")
				}
			}

			// 错误返回介入

			if err != nil {
				if onError != nil {
					retErr = onError(err)
				} else {
					if codeError, ok := err.(code.Error); ok {
						retErr = codeError
					} else {
						retErr = code.NewMcode("UNKNOWN_ERROR", err.Error())
					}
				}
			}

			var resp interface{}
			status := 200
			// 错误的返回
			if retErr != nil {
				resp = NewErrorResponse(retErr)
				status = defaultErrorStatus
				if statusError, ok := retErr.(StatusError); ok {
					status = statusError.HttpStatus()
				}
			} else {
				resp = NewOkResponse(data)
			}

			httpCtx.JSON(status, resp)

			if logMode > 0 {
				l := log.WithFields(logrus.Fields{
					"method":          httpCtx.Request.Method,
					"path":            httpCtx.Request.URL.Path,
					"delay":           time.Since(since),
					"query":           httpCtx.Request.URL.RawQuery,
					"pendingRequests": pendingRequests,
				})

				if retErr != nil && logMode&LogTypeError != 0 {
					l = l.WithFields(logrus.Fields{
						"mcode":   retErr.Mcode(),
						"message": retErr.Message(),
						"status":  status,
					})
					l.Error("HTTP request failed")
				} else if logMode&LogTypeSuccess != 0 {
					l.Info("HTTP request done")
				}
			}
		}()

		// 请求限制
		if maxPendingRequest > 0 {
			for err == nil {
				pending = atomic.LoadInt64(&pendingRequests)
				if pending+int64(requestWeight) > int64(maxPendingRequest) {
					err = ErrExceedMaxPendingRequest
					return
				}

				if !atomic.CompareAndSwapInt64(&pendingRequests, pending, pending+int64(requestWeight)) {
					continue
				}

				break
			}
		}

		if err != nil {
			return
		}

		defer atomic.AddInt64(&pendingRequests, int64(-requestWeight))

		data, err = f(httpCtx)
	}
}

func (wrapper *Wrapper) Handle(method string, srv HttpServer, path string, f WrappedFunc, options ...*WrapOption) {
	absPath := srv.(*gin.RouterGroup).BasePath() + path
	srv.Handle(method, path, wrapper.Wrap(f, absPath, options...))
}

func (wrapper *Wrapper) Get(srv HttpServer, path string, f WrappedFunc, options ...*WrapOption) {
	wrapper.Handle("GET", srv, path, f, options...)
}

func (wrapper *Wrapper) Any(srv HttpServer, path string, f WrappedFunc, options ...*WrapOption) {
	wrapper.Handle("GET", srv, path, f, options...)
	wrapper.Handle("POST", srv, path, f, options...)
	wrapper.Handle("PUT", srv, path, f, options...)
	wrapper.Handle("DELETE", srv, path, f, options...)
}

func (wrapper *Wrapper) Patch(srv HttpServer, path string, f WrappedFunc, options ...*WrapOption) {
	wrapper.Handle("PATCH", srv, path, f, options...)
}

func (wrapper *Wrapper) Post(srv HttpServer, path string, f WrappedFunc, options ...*WrapOption) {
	wrapper.Handle("POST", srv, path, f, options...)
}

func (wrapper *Wrapper) Put(srv HttpServer, path string, f WrappedFunc, options ...*WrapOption) {
	wrapper.Handle("PUT", srv, path, f, options...)
}

func (wrapper *Wrapper) Options(srv HttpServer, path string, f WrappedFunc, options ...*WrapOption) {
	wrapper.Handle("OPTIONS", srv, path, f, options...)
}

func (wrapper *Wrapper) Head(srv HttpServer, path string, f WrappedFunc, options ...*WrapOption) {
	wrapper.Handle("HEAD", srv, path, f, options...)
}

func (wrapper *Wrapper) Delete(srv HttpServer, path string, f WrappedFunc, options ...*WrapOption) {
	wrapper.Handle("DELETE", srv, path, f)
}

func (wrapper *Wrapper) LogNotProcess() func(ctx *gin.Context) {
	return func(ctx *gin.Context) {
		since := time.Now()
		ctx.Next()
		log := wrapper.cfg.log
		if ctx.Keys["easygin"] == nil && *wrapper.cfg.logMode&LogTypeError > 0 {
			l := log.WithFields(logrus.Fields{
				"method": ctx.Request.Method,
				"path":   ctx.Request.URL.Path,
				"delay":  time.Since(since),
				"query":  ctx.Request.URL.RawQuery,
				"status": ctx.Writer.Status(),
			})
			l.Error("HTTP request failed")
		}
	}
}
