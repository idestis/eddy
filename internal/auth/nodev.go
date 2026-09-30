//go:build !dev

package auth

import "net/http"

// devBuild is false in release builds: dev.fakeLogin makes New fail.
const devBuild = false

func (s *Service) devRoutes(*http.ServeMux) {}
