package rest

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	echoSwagger "github.com/swaggo/echo-swagger"
	"golang.org/x/time/rate"

	"github.com/cloud-barista/cm-centipede/pkg/api/rest/controller"
	"github.com/cloud-barista/cm-centipede/pkg/api/rest/middlewares"
	"github.com/cloud-barista/cm-centipede/pkg/config"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/rs/zerolog/log"
)

// Start initialises the Echo server, registers all routes, and blocks until
// SIGINT or SIGTERM is received. Graceful shutdown is performed with a 10-second
// timeout before returning control to the caller.
func Start() {
	e := echo.New()
	e.HideBanner = true

	// Global middleware
	e.Use(middlewares.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.RateLimiter(
		middleware.NewRateLimiterMemoryStore(rate.Limit(config.Conf.API.RateLimit)),
	))
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{config.Conf.API.Allow.Origins},
	}))

	gC := e.Group("/centipede")

	// Optional Basic Auth for the entire /centipede group
	if config.Conf.API.Auth.Enabled {
		gC.Use(middleware.BasicAuth(func(user, pass string, _ echo.Context) (bool, error) {
			return user == config.Conf.API.Username &&
				pass == config.Conf.API.Password, nil
		}))
	}

	// Utility
	gC.GET("/readyz", controller.Readyz)
	gC.GET("/api/*", echoSwagger.WrapHandler) // run `make swagger` to generate docs

	// Plan
	gPlan := gC.Group("/plans")
	gPlan.POST("/target", controller.GetTargetPlan)

	// Migration
	gMig := gC.Group("/migration")
	gMig.POST("", controller.CreateMigration)
	gMig.GET("", controller.ListMigration)
	gMig.GET("/all", controller.ListAllMigration)
	gMig.GET("/:migrationId", controller.GetMigration)
	gMig.DELETE("/:migrationId", controller.DeleteMigration)
	gMig.POST("/:migrationId/cancel", controller.CancelMigration)
	gMig.POST("/:migrationId/retry", controller.RetryMigration)
	gMig.GET("/:migrationId/logs", controller.ListMigrationLogs)
	gMig.POST("/:migrationId/validation", controller.StartValidation)
	gMig.GET("/:migrationId/validation", controller.GetValidation)

	addr := ":" + portFrom(config.Conf.Self.Endpoint)
	go func() {
		log.Info().Str("addr", addr).Msg("starting HTTP server")
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server error")
		}
	}()

	// Block until termination signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("shutting down server")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := e.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("server shutdown error")
	}
}

// portFrom extracts the port from a "host:port" endpoint string.
// If no colon is found the entire string is treated as the port.
func portFrom(endpoint string) string {
	for i := len(endpoint) - 1; i >= 0; i-- {
		if endpoint[i] == ':' {
			return endpoint[i+1:]
		}
	}
	return endpoint
}
