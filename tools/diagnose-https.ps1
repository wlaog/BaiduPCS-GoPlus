$ErrorActionPreference = 'Continue'
Write-Output "=== force TLS 1.2/1.3 and retry ==="
try {
  [System.Net.ServicePointManager]::SecurityProtocol = [System.Net.SecurityProtocolType]::Tls12
  Write-Output "SecurityProtocol set to Tls12"
} catch { Write-Output "set failed: $_" }
$urls = @(
  'https://go.dev/dl/?mode=json',
  'https://storage.googleapis.com/golang/go1.23.12.windows-amd64.zip',
  'https://golang.google.cn/dl/?mode=json',
  'https://mirrors.aliyun.com/golang/?mode=json'
)
foreach ($u in $urls) {
  try {
    $req = [System.Net.HttpWebRequest]::Create($u)
    $req.Method = 'GET'
    $req.Timeout = 45000
    $req.ReadWriteTimeout = 45000
    $req.UserAgent = 'Mozilla/5.0'
    $resp = $req.GetResponse()
    $code = [int]$resp.StatusCode
    $len = $resp.ContentLength
    $sr = New-Object System.IO.StreamReader($resp.GetResponseStream())
    $body = $sr.ReadToEnd()
    $sr.Close(); $resp.Close()
    Write-Output ("OK   {0} -> http={1} contentLength={2} bodyLen={3}" -f $u, $code, $len, $body.Length)
    if ($body.Length -gt 0 -and $body.Length -lt 4000) { Write-Output ("     body: " + ($body -replace "`r?`n", ' ').Substring(0, [Math]::Min(300, $body.Length))) }
  } catch {
    Write-Output ("FAIL {0} -> {1}" -f $u, $_.Exception.Message)
  }
}
Write-Output "=== git-based mirror test (gitee/go mirror over git) ==="
$g = & git ls-remote https://gitee.com/mirrors/go.git HEAD 2>&1
Write-Output (($g | Select-Object -First 2) -join "`n")
