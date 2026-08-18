# 📝 Changelog

All notable changes to this project will be documented in this file.

---

## [1.0.0] - 2024-08-16

### Added
- Initial release
- Go backend with REST API
- Dark theme web interface
- Model selector dropdown
- Streaming chat via SSE
- Process management (one model at a time)
- Graceful shutdown handler
- Health check polling
- Configuration via models.json
- Support for Phi-4 Mini and Mistral 7B models
- Comprehensive documentation

### Technical Details
- Backend: Go 1.21+ with net/http
- Frontend: Vanilla HTML/CSS/JS (no frameworks)
- Model runner: llama.cpp (llama-server)
- Streaming: Server-Sent Events (SSE)
- Config: JSON format

---

## Future Plans

- [ ] Chat history persistence (optional)
- [ ] Model parameter tuning (temperature, top_p, etc.)
- [ ] Multiple concurrent model support
- [ ] System prompt configuration
- [ ] Export chat as markdown
- [ ] Model performance metrics
- [ ] GPU acceleration settings
- [ ] Authentication for remote access
- [ ] HTTPS support via reverse proxy
- [ ] Docker container option
