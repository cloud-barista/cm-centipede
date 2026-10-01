package middlewares

import (
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"
)

// Logger returns an Echo middleware that logs each request via zerolog.
func Logger() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			start := time.Now()
			err := next(c)
			req := c.Request()
			res := c.Response()

			ev := log.Info().
				Str("method", req.Method).
				Str("uri", req.RequestURI).
				Int("status", res.Status).
				Dur("latency", time.Since(start)).
				Str("remote_ip", c.RealIP())
			if err != nil {
				ev = ev.Err(err)
			}
			ev.Msg("request")

			return err
		}
	}
}
