param(
    [string]$BaseUrl = "http://localhost:8080"
)

Write-Host "Running Concurrency Burst against $BaseUrl..."
go run cmd/burst/main.go $BaseUrl