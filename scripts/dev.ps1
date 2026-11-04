param(
    [switch]$SkipMigrate,
    [string]$EnvFile = '.env.local'
)

$ErrorActionPreference = 'Stop'
$allowedEnvironmentNames = @(
    'DATABASE_URL', 'DB_HOST', 'DB_PORT', 'DB_NAME', 'DB_USER', 'DB_PASSWORD', 'DB_SSLMODE',
    'APP_ADDR', 'APP_API_KEY', 'AUTO_MIGRATE', 'BUSINESS_TIME_ZONE', 'WEB_DIR'
)

function Import-LocalEnv([string]$FilePath) {
    if (-not (Test-Path -LiteralPath $FilePath -PathType Leaf)) {
        return
    }

    $lineNumber = 0
    foreach ($line in Get-Content -LiteralPath $FilePath -Encoding UTF8) {
        $lineNumber++
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith('#')) {
            continue
        }

        $separator = $trimmed.IndexOf('=')
        if ($separator -lt 1) {
            throw "Invalid format in $FilePath line $lineNumber. Expected KEY=VALUE."
        }

        $name = $trimmed.Substring(0, $separator).Trim()
        $value = $trimmed.Substring($separator + 1).Trim()
        if ($name -notmatch '^[A-Za-z_][A-Za-z0-9_]*$') {
            throw "Invalid environment variable name in $FilePath line $lineNumber."
        }
        if ($allowedEnvironmentNames -notcontains $name) {
            throw "Environment variable $name is not allowed in $FilePath."
        }

        # Explicit process variables take precedence over values in the local file.
        if (-not [Environment]::GetEnvironmentVariable($name, 'Process')) {
            Set-Item -Path "Env:$name" -Value $value
        }
    }
}

Import-LocalEnv $EnvFile

$hasDatabaseURL = -not [string]::IsNullOrWhiteSpace($env:DATABASE_URL)
$hasDatabaseParts = -not [string]::IsNullOrWhiteSpace($env:DB_HOST) -and `
    -not [string]::IsNullOrWhiteSpace($env:DB_NAME) -and `
    -not [string]::IsNullOrWhiteSpace($env:DB_USER) -and `
    -not [string]::IsNullOrWhiteSpace($env:DB_PASSWORD)

if (-not $hasDatabaseURL -and -not $hasDatabaseParts) {
    throw 'Copy .env.local.example to .env.local and set DB_HOST, DB_NAME, DB_USER, and DB_PASSWORD; or set DATABASE_URL in this terminal.'
}

if ($SkipMigrate) {
    $env:AUTO_MIGRATE = 'false'
}

if (-not $env:APP_ADDR) {
    $env:APP_ADDR = '127.0.0.1:8080'
}

go run ./cmd/server
