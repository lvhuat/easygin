package main

import (
	"fmt"
	"time"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/lvhuat/code"
	"github.com/lvhuat/easygin"
	"github.com/sirupsen/logrus"
)

type CodeError code.Error
type errorWithStatus struct {
	CodeError
}

func (status *errorWithStatus) HttpStatus() int {
	return 501
}

func main() {
	log := logrus.WithField("pkg", "easygin")
	engine := gin.New()
	app := easygin.New(&easygin.Config{
		WrapOption: *easygin.
			NewWrapOption().
			LogEntry(log).                                          // 采用的日志entry
			LogMode(easygin.LogTypeError | easygin.LogTypeSuccess). // optional logger
			MaxPendingRequests(100).                                // max pending requests of every endpoint
			ErrorStatus(303).                                       // default http status return when an error return
			ConvertError(func(err error) code.Error {
				if codeErr, ok := err.(code.Error); !ok {
					return code.NewMcode("XXX_ERROR", err.Error())
				} else {
					return codeErr
				}
			}).
			OnRecoverError(func(r interface{}) error {
				if codeErr, ok := r.(code.Error); !ok {
					return code.NewMcode("PANIC_ERROR", fmt.Sprintf("%v", r))
				} else {
					return codeErr
				}
			}),
		GinEngine: engine,
	})

	gin.SetMode(gin.ReleaseMode)
	//engine.Use(gin.Logger())
	//engine.Use(app.LogNotProcess())
	engine.Use(gzip.Gzip(gzip.DefaultCompression))

	group := engine.Group("")
	app.Any(group, "/sleep", func(ctx *gin.Context) (interface{}, error) {
		time.Sleep(time.Second * 10)
		return nil, nil
	})

	app.Any(group, "/ping", func(ctx *gin.Context) (interface{}, error) {
		if ctx.Query("error") == "1" {
			return nil, fmt.Errorf("not code error")
		}
		if ctx.Query("error") == "2" {
			return nil, code.NewMcode("CODE_ERROR", "code error")
		}

		if ctx.Query("error") == "3" {
			statusError := &errorWithStatus{
				CodeError: code.NewMcode("CODE_ERROR", "code error"),
			}
			return nil, statusError
		}

		if ctx.Query("error") == "4" {
			panic("panic string error")
		}

		if ctx.Query("error") == "5" {
			statusError := &errorWithStatus{
				CodeError: code.NewMcode("PANIC_CODE_ERROR_WITH_STATUS", "code error"),
			}
			panic(statusError)
		}

		if ctx.Query("success") == "2" {
			return map[string]string{
				"returnValue": "1",
			}, nil
		}

		if ctx.Query("success") == "3" {
			return []string{"item1", "item2"}, nil
		}

		if ctx.Query("success") == "4" {
			return "item", nil
		}

		return nil, nil
	}, easygin.NewWrapOption().LogMode(easygin.LogTypeError))

	engine.Run(":8080")
}
