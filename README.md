# Kadi Backend

This is the high-performance Golang backend for the Kadi sports analytics and prediction platform.

## Architecture Highlights
- **Language**: Go (Golang) 1.23
- **Database**: PostgreSQL via Supabase Connection Pooler (using `pgxpool`)
- **API Framework**: Gin
- **AI Engine**: Google Gemini (1.5-flash) via official SDK
- **Real-Time Strategy**: Stateless API structure. Golang persists state changes into Postgres, and Supabase handles websocket broadcasting to Next.js clients via Realtime.
- **Workers**: Concurrency-managed background processes using goroutines for daily odds/fixtures sync and live ticker updates.

## Getting Started

1. **Environment Variables**: Copy `.env.example` to `.env` and fill in the necessary keys.
2. **Database Setup**: Run the SQL commands in `supabase/schema.sql` inside your Supabase project's SQL editor to set up the tables, RLS, and realtime publications.
3. **Run locally**:
   ```bash
   go run cmd/server/main.go
   ```

## Docker Deployment
The backend uses a highly-optimized, multi-stage Docker build resulting in a secure, minimal `scratch` image.
```bash
docker build -t kadi-backend .
docker run --env-file .env -p 8080:8080 kadi-backend
```
