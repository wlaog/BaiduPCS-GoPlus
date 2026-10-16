package pcscommand

import (
	"html/template"
	"strings"
)

// guiPageData feeds the single embedded page.
type guiPageData struct {
	Token      string
	LocalDir   string
	RemoteRoot string
	Version    string
}

// renderGuiPage renders the whole interface as one self-contained HTML document.
//
// The page is embedded rather than loaded from disk so the binary stays a single
// file, and it uses no external assets so it works with no network access.
func renderGuiPage(token, localDir, remoteRoot string) string {
	tmpl := template.Must(template.New("gui").Parse(guiPageTemplate))
	var sb strings.Builder
	_ = tmpl.Execute(&sb, guiPageData{
		Token:      token,
		LocalDir:   localDir,
		RemoteRoot: remoteRoot,
		Version:    guiVersion,
	})
	return sb.String()
}

const guiPageTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>对齐本地 · BaiduPCS-GoPlus</title>
<style>
:root{
  --bg:#f5f6f8; --card:#fff; --line:#e2e5ea; --text:#1f2328; --muted:#656d76;
  --accent:#1668dc; --ok:#1a7f37; --warn:#9a6700; --danger:#cf222e; --add:#1a7f37; --del:#cf222e;
}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--text);
  font:14px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI","Microsoft YaHei",sans-serif}
.wrap{max-width:960px;margin:0 auto;padding:32px 20px 48px}
header{display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;margin-bottom:6px}
h1{font-size:20px;margin:0}
h2{font-size:15px;margin:0 0 10px;display:flex;align-items:center;gap:8px}
.sub{color:var(--muted);font-size:13px}
.card{background:var(--card);border:1px solid var(--line);border-radius:10px;
  padding:16px 18px;margin-bottom:14px}
.hint{color:var(--muted);font-size:13px}
.banner{border-left:4px solid var(--accent);background:#eef4ff;padding:10px 14px;
  border-radius:6px;margin:12px 0;font-size:13px}
.banner.warn{border-color:var(--warn);background:#fff8e6}
label{display:block;font-weight:600;margin:10px 0 4px;font-size:13px}
input[type=text],input[type=number],select{width:100%;padding:8px 10px;border:1px solid var(--line);
  border-radius:6px;font:inherit;background:#fff}
.row{display:flex;gap:14px;flex-wrap:wrap}
.col{flex:1 1 320px;min-width:0}
button{padding:9px 16px;border:1px solid var(--line);background:#fff;border-radius:6px;
  font:inherit;cursor:pointer}
button:hover:not(:disabled){border-color:var(--accent);color:var(--accent)}
button:disabled{opacity:.45;cursor:not-allowed}
button.primary{background:var(--accent);border-color:var(--accent);color:#fff}
button.primary:hover:not(:disabled){opacity:.9;color:#fff}
button.danger{background:var(--danger);border-color:var(--danger);color:#fff}
button.danger:hover:not(:disabled){opacity:.9;color:#fff}
.actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:14px}
.steps{counter-reset:s;list-style:none;padding:0;margin:0}
.steps>li{counter-increment:s;position:relative;padding:12px 0 12px 42px;
  border-bottom:1px dashed var(--line);line-height:1.85}
.steps>li:last-child{border-bottom:none}
.steps>li::before{content:counter(s);position:absolute;left:0;top:13px;width:26px;height:26px;
  border-radius:50%;background:var(--accent);color:#fff;display:flex;align-items:center;
  justify-content:center;font-size:13px;font-weight:700}
.steps b{display:block;margin-bottom:2px}
/* Inline code must not wrap mid-token: breaking a path or command across lines
   makes the instructions hard to follow, and Chinese text around it has no
   spaces to break at. */
.steps code, li code, p code, .hint code{
  background:#f2f4f7;padding:1px 6px;border-radius:4px;font-size:12px;
  white-space:nowrap;word-break:keep-all;font-family:Consolas,"Courier New",monospace}
.steps .hint{display:block;margin-top:4px}
ul.tips{margin:6px 0 0;padding-left:22px;line-height:1.85}
ul.tips li{margin-bottom:4px}
table{width:100%;border-collapse:collapse;font-size:13px}
th,td{text-align:left;padding:6px 8px;border-bottom:1px solid var(--line);
  vertical-align:top;word-break:break-all}
th{color:var(--muted);font-weight:600;background:#fafbfc}
.tally{display:flex;gap:20px;flex-wrap:wrap;margin-bottom:12px}
.tally div{background:#fafbfc;border:1px solid var(--line);border-radius:8px;padding:8px 14px}
.tally b{display:block;font-size:19px}
.k-UPLOAD{color:var(--add);font-weight:700}
.k-REPLACE{color:var(--warn);font-weight:700}
.k-REMOVE{color:var(--del);font-weight:700}
.k-KEEP{color:var(--muted);font-weight:700}
.k-CONFLICT{color:var(--del);font-weight:700}
.k-MKDIR{color:var(--accent);font-weight:700}
pre#log{background:#0d1117;color:#c9d1d9;padding:12px;border-radius:8px;max-height:260px;
  overflow:auto;font-size:12px;line-height:1.5;white-space:pre-wrap;margin:0}
.hidden{display:none}
.badge{display:inline-block;padding:1px 8px;border-radius:20px;font-size:12px;
  border:1px solid var(--line);background:#fafbfc;color:var(--muted)}
.badge.ok{color:var(--ok);border-color:#b7e0c0;background:#eaf7ed}
.badge.bad{color:var(--danger);border-color:#f5c2c7;background:#fff0f1}
details summary{cursor:pointer;font-weight:600;font-size:13px;padding:4px 0}
footer{color:var(--muted);font-size:12px;text-align:center;margin-top:24px}
.intro{color:var(--muted);margin:8px 0 24px}
header .sub{margin-left:auto} header button{padding:4px 10px;font-size:12px}
.card{padding:20px 24px;border-radius:14px;margin-bottom:16px}
h2{font-size:16px;margin-bottom:16px}.step{display:inline-grid;place-items:center;width:26px;height:26px;border-radius:8px;background:#eef4ff;color:var(--accent);font-size:13px}
.folder-field{display:flex;gap:8px;align-items:center}.folder-field input{min-width:0;flex:1}
.folder-field button{white-space:nowrap;padding:8px 14px}
input:focus,select:focus,button:focus-visible,summary:focus-visible{outline:2px solid #a9c9ff;outline-offset:2px}
.check{display:flex;align-items:flex-start;gap:8px;font-weight:400;line-height:1.7}
.check input{margin-top:5px;flex-shrink:0}.field-note{margin:6px 0 0;font-size:12px;color:var(--muted)}
.advanced{margin-top:14px}.advanced summary{font-weight:400;color:var(--muted)}
.details-body{padding-top:14px}.help{padding:12px 20px}.help>summary{font-weight:500}
.help .steps>li{padding-top:8px;padding-bottom:8px}.help .steps>li::before{top:10px}
.banner p{margin:6px 0}.tally{gap:8px}.tally div{flex:1;min-width:95px;padding:8px 12px}.tally b{font-size:22px}
.table-wrap{overflow:auto;max-height:360px;border:1px solid var(--line);border-radius:8px;margin-top:8px}
.table-wrap th{position:sticky;top:0}.table-wrap table{margin:0}
dialog{width:min(620px,calc(100% - 32px));padding:0;border:1px solid var(--line);border-radius:16px;color:var(--text);box-shadow:0 24px 80px #0003}
dialog::backdrop{background:#18223066} .picker-head,.picker-foot{padding:18px 22px;display:flex;align-items:center;gap:10px}
.picker-head{border-bottom:1px solid var(--line)}.picker-head h2{margin:0;flex:1}.picker-head button{padding:3px 10px}
.picker-nav{display:flex;gap:6px;padding:14px 22px 8px}.picker-nav input{min-width:0;flex:1}.picker-nav button{padding:6px 10px;white-space:nowrap}
.picker-current{padding:0 22px 10px;color:var(--muted);font-size:12px;overflow-wrap:anywhere}
.picker-list{height:300px;overflow:auto;border-top:1px solid var(--line);border-bottom:1px solid var(--line);padding:6px 12px}
.folder-item{display:flex;align-items:center;text-align:left;gap:10px;width:100%;border:0;padding:11px 12px;border-radius:8px;overflow-wrap:anywhere}
.folder-item:hover:not(:disabled){background:#eef4ff}.folder-icon{color:#b8821e;font-size:18px}.folder-arrow{margin-left:auto;color:var(--muted)}
.picker-empty{padding:35px 12px;text-align:center;color:var(--muted)}.picker-error{margin:0 22px 12px;color:var(--danger);font-size:13px}
.picker-foot{justify-content:flex-end}.picker-foot .hint{margin-right:auto;font-size:12px}
@media(max-width:600px){.wrap{padding:18px 12px}.card{padding:16px}.row{gap:8px}.picker-nav{padding-left:12px;padding-right:12px}.picker-nav button{font-size:12px}.picker-foot{flex-wrap:wrap}.picker-foot .hint{flex-basis:100%}header .sub{margin-left:0}}
</style>
</head>
<body>
<div class="wrap">

<header>
  <h1>对齐本地</h1>
  <span class="badge" id="badgeAccount">账号状态检查中…</span>
  <span class="sub"><button id="btnQuit">关闭界面</button></span>
</header>

<p class="intro">以本地文件夹为准，手动整理对应的云端目录。先预览，确认后执行。</p>

<!-- ===================== 使用说明 ===================== -->
<details class="card help" id="usageGuide">
  <summary>使用说明与恢复方法</summary>
  <div class="details-body">
  <ol class="steps">
    <li>
      <b>确认已登录</b>
      本界面复用 BaiduPCS-Go 已配置的账号，右上角应显示绿色账号状态。
      若显示未登录，请先在命令行执行 <code>BaiduPCS-Go login</code>，
      再重新打开本界面。
    </li>
    <li>
      <b>找出云端备份目录（可选：只读探测）</b>
      勾选「扫描云端根目录」后点击 <b>开始只读探测</b>。它会列出云端根目录下
      形如 <code>来自：本地电脑</code> 的备份目录，并核对本地与云端。
      <span class="hint">探测是只读的，不会改动任何内容，可以放心反复执行。</span>
    </li>
    <li>
      <b>填写两个目录（一一对应）</b>
      选择或填写<b>本地目录</b>（权威源，例如 <code>D:\WorkSync</code>），
      以及<b>云端目录</b>（要整理成和本地一样的那个，例如
      <code>/来自：本地电脑/WorkSync</code>）。
      <span class="hint">云端目录必须是具体子目录，不能填 <code>/</code>。</span>
    </li>
    <li>
      <b>预览差异（第 1 步：只读）</b>
      点击 <b>预览差异</b>。它会逐项列出五类结果：
      <span class="k-UPLOAD">UPLOAD</span> 云端缺失、
      <span class="k-REPLACE">REPLACE</span> 同路径但内容不同、
      <span class="k-REMOVE">REMOVE</span> 云端多余、
      <span class="k-KEEP">KEEP</span> 已一致、
      <span class="k-CONFLICT">CONFLICT</span> 需人工判断。
      <span class="hint">勾选「计算内容哈希」判定更精确（会读取本地文件，稍慢）；
      无法确认内容一致时会保守地重新上传或标记冲突。</span>
    </li>
    <li>
      <b>检查预览结果</b>
      重点看 <b>REMOVE</b> 列表是不是真的都不需要了，以及有没有 <b>CONFLICT</b>。
      <span class="hint">已确认一致的项不会出现在变更里。本地真实存在的
      <code>paper(1).docx</code> 会被保留，不会因为名字像副本就被删除。</span>
    </li>
    <li>
      <b>执行对齐（第 2 步：会修改云端）</b>
      点击 <b>执行对齐</b> 并确认。执行顺序是：
      建目录 → 上传/覆盖 → <b>重新校验写入确实生效</b> → 处理多余项。
      任何一步失败都会<strong>跳过后续清理</strong>，因为云端副本只有在本地对应版本
      确认上传成功之后才算"多余"。
    </li>
    <li>
      <b>需要时恢复</b>
      默认清理方式是<b>隔离区</b>：多余项被移动到
      <code>/BackupReconcile_Quarantine/&lt;时间戳&gt;/</code>，保留原相对路径，
      在网盘里移回原位置即可恢复。<b>不会永久删除，也不会清空回收站。</b>
    </li>
    <li>
      <b>确认结果</b>
      执行完成后再次点击 <b>预览差异</b>，应当显示<b>零差异</b>。
      每次探测 / 预览 / 执行都会保存 JSON 记录，结果中会显示报告路径。
    </li>
  </ol>

  <details>
    <summary>安全设计说明（建议读一遍）</summary>
    <ul>
      <li><b>只读优先：</b>探测和预览完全不写云端，可反复执行。</li>
      <li><b>不做永久删除：</b>清理只有两种方式 —— 移入隔离区（默认）或移入网盘回收站，都需要显式确认。</li>
      <li><b>不触碰本地文件：</b>本地目录只被读取（大小、修改时间、哈希），绝不会被修改或删除。</li>
      <li><b>不越界：</b>所有操作都被限制在你填写的云端目录之内。</li>
      <li><b>阈值保护：</b>一次性清理超过云端文件数 10% 或超过 10GB 时，需要额外勾选确认。</li>
      <li><b>半写入保护：</b>若某文件在预览之后又被修改，执行时会跳过它并提示，不会把写了一半的文件当定稿上传。</li>
      <li><b>不完整即停止：</b>若云端目录枚举不完整（网络/限流/权限），直接拒绝执行，不做任何改动。</li>
      <li><b>官方自动备份：</b>仍由百度网盘官方客户端管理；执行整理前请确认对应任务没有同时上传。</li>
    </ul>
  </details>
  </div>
</details>

<!-- ===================== 第 1 步 探测 ===================== -->
<details class="card help" id="probePanel">
  <summary>找不到云端备份目录？展开只读探测</summary>
  <div class="details-body">
  <label>云端目录（留空并勾选下方选项即可自动发现）</label>
  <div class="folder-field"><input type="text" id="probeRemote" placeholder="/来自：本地电脑/WorkSync"><button data-browse="probeRemote" data-kind="remote">浏览…</button></div>
  <label>本地目录（可选，填写后会比对缺失项与内容哈希）</label>
  <div class="folder-field"><input type="text" id="probeLocal" placeholder="D:\WorkSync"><button data-browse="probeLocal" data-kind="local">浏览…</button></div>
  <div class="row" style="margin-top:10px">
    <div class="col">
      <label style="margin-top:0"><input type="checkbox" id="probeDiscover" checked> 扫描云端根目录，自动发现备份目录</label>
    </div>
    <div class="col">
      <label style="margin-top:0">哈希抽样数量
        <input type="number" id="probeSamples" value="10" min="0" max="200" style="width:110px;display:inline-block;margin-left:6px">
      </label>
    </div>
  </div>
  <div class="actions">
    <button class="primary" id="btnProbe">开始只读探测</button>
  </div>
  <div id="probeOut" class="hidden" style="margin-top:14px"></div>
  </div>
</details>

<!-- ===================== 第 2 步 预览 ===================== -->
<div class="card">
  <h2><span class="step">1</span> 选择目录并预览 <span class="badge">只读</span></h2>
  <div class="row">
    <div class="col">
      <label for="localDir">本地文件夹 · 以这里为准</label>
      <div class="folder-field"><input type="text" id="localDir" placeholder="D:\WorkSync" value="{{.LocalDir}}"><button data-browse="localDir" data-kind="local">浏览…</button></div>
      <p class="field-note">只读取本地文件，不修改本地内容。</p>
    </div>
    <div class="col">
      <label for="remoteRoot">云端文件夹 · 要整理的备份目录</label>
      <div class="folder-field"><input type="text" id="remoteRoot" placeholder="/来自：本地电脑/WorkSync" value="{{.RemoteRoot}}"><button data-browse="remoteRoot" data-kind="remote">浏览…</button></div>
      <p class="field-note">请选择与本地对应的具体子目录。</p>
    </div>
  </div>
  <details class="advanced"><summary>比较选项</summary>
    <label class="check"><input type="checkbox" id="hashFiles" checked>核对文件内容（推荐；读取本地文件计算哈希）</label>
    <label class="check"><input type="checkbox" id="conservative">无法确认内容一致时，留待人工处理</label>
  </details>
  <div class="actions">
    <button class="primary" id="btnPlan">预览差异</button>
  </div>
  <div id="planOut" class="hidden" style="margin-top:14px"></div>
</div>

<!-- ===================== 第 3 步 执行 ===================== -->
<div class="card">
  <h2><span class="step">2</span> 确认并执行 <span class="badge bad">会修改云端</span></h2>
  <div id="applyBlocked" class="hint">请先选择两个目录并预览差异。</div>
  <div id="applyReady" class="hidden">
    <div id="cleanupSummary" class="banner"></div>
    <label>清理方式</label>
    <select id="removalMode">
      <option value="quarantine">移入隔离区（推荐，可移回恢复）</option>
      <option value="recycle">移入网盘回收站（恢复期限以网盘为准）</option>
    </select>
    <details class="advanced" id="quarantineOptions"><summary>自定义隔离区位置</summary>
      <label for="quarantineRoot">隔离区目录（留空使用默认位置）</label>
      <input type="text" id="quarantineRoot" placeholder="/BackupReconcile_Quarantine">
    </details>
    <div style="margin-top:10px">
      <label class="check"><input type="checkbox" id="confirmRemoval">同时清理预览中列出的云端多余文件</label>
      <p class="field-note">不勾选则只上传和覆盖，保留云端多余文件。</p>
    </div>
    <div id="blastWarning" class="banner warn hidden">
      <strong>本次清理较多，需要再确认一次</strong>
      <p id="blastSummary"></p>
      <p>当清理文件数超过此云端目录文件总数的 10%，或清理容量超过 10 GB 时，会触发此保护。例如：云端有 100 个文件，清理 11 个就需要额外确认。</p>
      <label class="check"><input type="checkbox" id="ackBlast">我已核对预览中的多余文件，确认继续本次大批量清理</label>
    </div>
    <div class="actions">
      <button class="danger" id="btnApply">执行对齐</button>
    </div>
  </div>
  <div id="applyOut" class="hidden" style="margin-top:14px"></div>
</div>

<details class="card help">
  <summary>运行日志</summary>
  <div class="details-body">
  <pre id="log">等待操作…</pre>
  <div class="actions">
    <button id="btnLog">刷新日志</button>
  </div>
  </div>
</details>

<footer>对齐本地 · BaiduPCS-GoPlus v{{.Version}} — 本机界面，只监听 127.0.0.1，所有请求都需要令牌。</footer>
</div>

<dialog id="folderPicker" aria-labelledby="pickerTitle">
  <div class="picker-head"><h2 id="pickerTitle">选择文件夹</h2><button id="pickerClose" aria-label="关闭目录选择">×</button></div>
  <form class="picker-nav" id="pickerNav">
    <button type="button" id="pickerHome">起点</button><button type="button" id="pickerUp">上一级</button>
    <input id="pickerPath" type="text" aria-label="目录路径" autocomplete="off" placeholder="输入路径后回车"><button type="submit">前往</button>
  </form>
  <div class="picker-current" id="pickerCurrent" aria-live="polite"></div>
  <div class="picker-list" id="pickerList" aria-label="子文件夹"></div>
  <p class="picker-error" id="pickerError" role="alert"></p>
  <div class="picker-foot"><span class="hint">点击文件夹进入，再选择当前文件夹。</span><button id="pickerCancel">取消</button><button class="primary" id="pickerSelect" disabled>选择此文件夹</button></div>
</dialog>

<script>
const TOKEN = {{.Token}};
const api = (p) => p + "?token=" + encodeURIComponent(TOKEN);

function esc(s){ return String(s==null?"":s).replace(/[&<>"']/g, c =>
  ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c])); }
function size(n){
  if(n==null||n===0) return n===0 ? "0 B" : "";
  const u=["B","KB","MB","GB","TB"]; let i=0,v=n;
  while(v>=1024&&i<u.length-1){v/=1024;i++;}
  return v.toFixed(i===0?0:2)+" "+u[i];
}
async function request(path, options){
  let r;
  try{ r = await fetch(api(path), options); }
  catch(e){ throw new Error("与本地服务的连接已中断。操作可能已部分完成，请检查启动窗口和运行日志；恢复连接后重新预览差异，不要直接重复执行。"); }
  let j;
  try{ j = await r.json(); }
  catch(e){ throw new Error("本地服务未返回有效结果（HTTP "+r.status+"），请查看启动窗口并重新预览差异。"); }
  if(!r.ok) throw new Error(j.error || "本地服务请求失败（HTTP "+r.status+"），请查看运行日志。");
  return j;
}
async function post(path, body){
  try{
    return await request(path, {method:"POST", headers:{"Content-Type":"application/json"},
      body: JSON.stringify(body||{})});
  }catch(e){ return {ok:false, error:e.message}; }
}
function show(id, html){ const el=document.getElementById(id); el.innerHTML=html; el.classList.remove("hidden"); }
function busy(btn, on, label){
  btn.disabled = on;
  if(on){ btn.dataset.orig = btn.textContent; btn.textContent = label || "处理中…"; }
  else if(btn.dataset.orig){ btn.textContent = btn.dataset.orig; }
}

let currentPlan = null;
let planEpoch = 0;
function invalidatePlan(){
  planEpoch++;
  currentPlan = null;
  document.getElementById("applyReady").classList.add("hidden");
  document.getElementById("applyBlocked").classList.remove("hidden");
  document.getElementById("applyBlocked").textContent = "目录或比较选项已改变，请重新预览差异。";
  document.getElementById("planOut").classList.add("hidden");
  document.getElementById("applyOut").classList.add("hidden");
  document.getElementById("confirmRemoval").checked = false;
  document.getElementById("ackBlast").checked = false;
}
for(const id of ["localDir", "remoteRoot", "hashFiles", "conservative"]){
  document.getElementById(id).addEventListener("input", invalidatePlan);
}
function removalStats(report){
  const s = report.summary;
  const percent = s.cloudFiles > 0 ? s.removeFiles / s.cloudFiles * 100 : 0;
  return {percent, exceeds: percent > 10 || s.removeBytes > 10*1024*1024*1024};
}
function updateCleanupOptions(){
  const mode = document.getElementById("removalMode").value;
  document.getElementById("quarantineOptions").classList.toggle("hidden", mode !== "quarantine");
  if(!currentPlan) return;
  const s = currentPlan.summary, risk = removalStats(currentPlan);
  const clean = document.getElementById("confirmRemoval").checked;
  const destination = mode === "quarantine" ? "隔离区" : "网盘回收站";
  document.getElementById("cleanupSummary").textContent = "本次将上传 " + currentPlan.counts.upload + " 个新文件、覆盖 " + currentPlan.counts.replace
    + " 个文件。云端多余：" + s.removeFiles + " 个文件、" + s.removeDirs + " 个目录（" + size(s.removeBytes) + "）。"
    + (clean ? "多余项将移入" + destination + "。" : "当前保留云端多余项。");
  document.getElementById("blastSummary").textContent = "拟清理 " + s.removeFiles + " / " + s.cloudFiles
    + " 个云端文件（" + risk.percent.toFixed(1) + "%），合计 " + size(s.removeBytes) + "。";
  document.getElementById("blastWarning").classList.toggle("hidden", !clean || !risk.exceeds);
  document.getElementById("ackBlast").disabled = !clean || !risk.exceeds;
  document.getElementById("btnApply").disabled = clean && risk.exceeds && !document.getElementById("ackBlast").checked;
  document.getElementById("btnApply").textContent = clean ? "执行对齐并清理" : "执行上传与覆盖";
}
document.getElementById("confirmRemoval").onchange = () => {
  document.getElementById("ackBlast").checked = false;
  updateCleanupOptions();
};
document.getElementById("ackBlast").onchange = updateCleanupOptions;
document.getElementById("removalMode").onchange = () => {
  document.getElementById("ackBlast").checked = false;
  updateCleanupOptions();
};

// ---------- folder browser (read-only) ----------
const picker = document.getElementById("folderPicker");
let pickerState = null, pickerLoad = 0;
async function loadFolders(dir){
  const generation = ++pickerLoad;
  const state = pickerState;
  document.getElementById("pickerSelect").disabled = true;
  document.getElementById("pickerUp").disabled = true;
  document.getElementById("pickerError").textContent = "";
  document.getElementById("pickerList").innerHTML = '<div class="picker-empty">正在读取文件夹…</div>';
  const j = await post("/api/browse", {kind: state.kind, path: dir});
  if(generation !== pickerLoad || !picker.open) return;
  const list = document.getElementById("pickerList");
  list.replaceChildren();
  if(!j.ok){
    document.getElementById("pickerError").textContent = j.error;
    document.getElementById("pickerCurrent").textContent = "读取失败，可修改路径或返回起点。";
    return;
  }
  Object.assign(state, j.listing);
  document.getElementById("pickerPath").value = state.path;
  document.getElementById("pickerCurrent").textContent = state.path || "此电脑 · 选择磁盘";
  document.getElementById("pickerUp").disabled = state.path === "" || state.path === "/";
  document.getElementById("pickerSelect").disabled = !state.selectable;
  for(const folder of state.folders){
    const button = document.createElement("button");
    button.className = "folder-item";
    const icon = document.createElement("span"); icon.className = "folder-icon"; icon.textContent = "▰";
    const name = document.createElement("span"); name.textContent = folder.name;
    const arrow = document.createElement("span"); arrow.className = "folder-arrow"; arrow.textContent = "›";
    button.append(icon, name, arrow);
    button.onclick = () => loadFolders(folder.path);
    list.append(button);
  }
  if(!state.folders.length){
    list.innerHTML = '<div class="picker-empty">没有子文件夹，可以选择当前文件夹。</div>';
  }
  if(state.kind === "remote" && state.path === "/"){
    document.getElementById("pickerCurrent").textContent = "网盘根目录 · 请进入具体的备份文件夹后选择";
  }
}
function openFolderPicker(target, kind){
  pickerState = {target, kind, path: "", parent: "", selectable: false};
  document.getElementById("pickerTitle").textContent = kind === "local" ? "选择本地文件夹" : "选择云端文件夹";
  document.getElementById("pickerHome").textContent = kind === "local" ? "此电脑" : "网盘根目录";
  document.getElementById("pickerPath").value = document.getElementById(target).value.trim();
  picker.showModal();
  loadFolders(document.getElementById(target).value.trim());
}
document.querySelectorAll("[data-browse]").forEach(button => {
  button.onclick = () => openFolderPicker(button.dataset.browse, button.dataset.kind);
});
document.getElementById("pickerNav").onsubmit = ev => {ev.preventDefault(); loadFolders(document.getElementById("pickerPath").value.trim());};
document.getElementById("pickerPath").addEventListener("input", () => {document.getElementById("pickerSelect").disabled = true;});
document.getElementById("pickerHome").onclick = () => loadFolders("");
document.getElementById("pickerUp").onclick = () => loadFolders(pickerState.parent);
document.getElementById("pickerClose").onclick = () => picker.close();
document.getElementById("pickerCancel").onclick = () => picker.close();
picker.addEventListener("close", () => {pickerLoad++;});
document.getElementById("pickerSelect").onclick = () => {
  if(document.getElementById("pickerSelect").disabled || !pickerState.selectable) return;
  const input = document.getElementById(pickerState.target);
  if(input.value !== pickerState.path){ input.value = pickerState.path; input.dispatchEvent(new Event("input", {bubbles:true})); }
  picker.close(); input.focus();
};

// ---------- account ----------
async function refreshAccount(){
  const b = document.getElementById("badgeAccount");
  try{
    const j = await request("/api/state");
    b.title = j.account ? "配置目录：" + j.account.configDir : "";
    if(j.account && j.account.loggedIn){
      b.textContent = "已登录: " + j.account.name;
      b.className = "badge ok";
    } else {
      b.textContent = "未登录 — 请使用当前配置目录登录 BaiduPCS-Go";
      b.className = "badge bad";
    }
  }catch(e){
    b.textContent = "账号状态读取失败";
    b.className = "badge bad";
    b.title = e.message;
  }
}

// ---------- probe ----------
document.getElementById("btnProbe").onclick = async (ev) => {
  const btn = ev.target; busy(btn, true, "探测中…");
  try{
    const j = await post("/api/probe", {
      remoteRoot: document.getElementById("probeRemote").value.trim(),
      localDir: document.getElementById("probeLocal").value.trim(),
      hashSamples: parseInt(document.getElementById("probeSamples").value||"10",10),
      discoverRoots: document.getElementById("probeDiscover").checked
    });
    if(!j.ok){ show("probeOut", '<div class="banner warn">'+esc(j.error)+'</div>'); return; }
    const rep = j.report;
    let h = "";
    if(rep.rootCandidates && rep.rootCandidates.length){
      h += "<h3 style='font-size:14px'>发现疑似备份目录</h3><table><tr><th>云端路径</th><th>命中</th><th>子项</th></tr>";
      rep.rootCandidates.forEach(c => h += "<tr><td>"+esc(c.path)+"</td><td>"+esc(c.matched)+"</td><td>"+esc(c.children)+"</td></tr>");
      h += "</table>";
      h += '<div class="hint" style="margin-top:8px">把上面某个路径填入第 2 步的「云端目录」即可。</div>';
    }
    if(rep.scan){
      h += "<h3 style='font-size:14px;margin-top:14px'>云端枚举</h3>";
      h += "<div class='tally'><div><b>"+rep.scan.stats.dirCount+"</b>目录</div>"
        +  "<div><b>"+rep.scan.stats.fileCount+"</b>文件</div>"
        +  "<div><b>"+size(rep.scan.stats.totalBytes)+"</b>合计</div></div>";
    }
    if(rep.verdicts && rep.verdicts.length){
      h += "<h3 style='font-size:14px'>判定</h3><ul style='margin:0;padding-left:20px'>";
      rep.verdicts.forEach(v => h += "<li>"+esc(v)+"</li>");
      h += "</ul>";
    }
    if(rep.errors && rep.errors.length){
      h += '<div class="banner warn"><b>错误</b><ul style="margin:6px 0 0;padding-left:20px">';
      rep.errors.forEach(e => h += "<li>"+esc(e)+"</li>");
      h += "</ul></div>";
    }
    if(j.savedTo) h += '<div class="hint" style="margin-top:8px">报告已保存: '+esc(j.savedTo)+'</div>';
    show("probeOut", h);
  } finally { busy(btn, false); refreshAccount(); }
};

// ---------- plan ----------
document.getElementById("btnPlan").onclick = async (ev) => {
  invalidatePlan();
  const epoch = planEpoch;
  const btn = ev.target; busy(btn, true, "扫描中…");
  try{
    const j = await post("/api/plan", {
      localDir: document.getElementById("localDir").value.trim(),
      remoteRoot: document.getElementById("remoteRoot").value.trim(),
      hashFiles: document.getElementById("hashFiles").checked,
      conservative: document.getElementById("conservative").checked
    });
    if(epoch !== planEpoch) return;
    if(!j.ok){
      show("planOut", '<div class="banner warn">'+esc(j.error)+'</div>');
      document.getElementById("applyReady").classList.add("hidden");
      document.getElementById("applyBlocked").classList.remove("hidden");
      return;
    }
    const r = j.report;
    currentPlan = r;
    let h = "<div class='tally'>"
      + "<div><b class='k-UPLOAD'>"+r.counts.upload+"</b>UPLOAD 待上传</div>"
      + "<div><b class='k-REPLACE'>"+r.counts.replace+"</b>REPLACE 待覆盖</div>"
      + "<div><b class='k-MKDIR'>"+r.counts.mkdir+"</b>MKDIR 待建目录</div>"
      + "<div><b class='k-REMOVE'>"+r.counts.remove+"</b>REMOVE 云端多余</div>"
      + "<div><b class='k-KEEP'>"+r.counts.keep+"</b>KEEP 已一致</div>"
      + "<div><b class='k-CONFLICT'>"+r.counts.conflict+"</b>CONFLICT 需判断</div>"
      + "</div>";
    h += "<div class='hint'>本地 "+r.summary.localFiles+" 文件 / 云端 "+r.summary.cloudFiles
      + " 文件；预计上传 "+size(r.summary.uploadBytes)+"，清理 "+r.summary.removeFiles+" 文件 ("
      + size(r.summary.removeBytes)+")。</div>";

    const section = (title, note, items) => {
      if(!items || !items.length) return "";
      let s = "<h3 style='font-size:14px;margin-top:14px'>"+title+" ("+items.length+")</h3>";
      if(note) s += "<div class='hint'>"+note+"</div>";
      s += "<div class='table-wrap'><table><tr><th>相对路径</th><th>大小</th><th>说明</th></tr>";
      items.slice(0,300).forEach(a => {
        s += "<tr><td>"+esc(a.relPath)+(a.isDir?"/":"")+"</td><td>"+size(a.size)+"</td><td>"
          + esc(a.reason||a.rule||"")+"</td></tr>";
      });
      if(items.length>300) s += "<tr><td colspan=3 class='hint'>其余 "+(items.length-300)+" 项见 JSON 报告</td></tr>";
      return s + "</table></div>";
    };
    h += section("UPLOAD 将要上传", "", r.uploads);
    h += section("REPLACE 将要覆盖", "覆盖会替换云端现有内容，请先核对差异。", r.replaces);
    h += section("MKDIR 将要创建的云端目录", "", r.mkdirs);
    h += section("REMOVE 云端多余项", "执行时默认移入隔离区，不永久删除。", r.removes);
    h += section("CONFLICT 需要你人工判断", "这些项不会被自动处理。", r.conflicts);

    if(r.blockers && r.blockers.length){
      h += '<div class="banner warn"><b>阻止项（执行前必须解决）</b><ul style="margin:6px 0 0;padding-left:20px">';
      r.blockers.forEach(b => h += "<li>"+esc(b)+"</li>");
      h += "</ul></div>";
    }
    if(j.savedTo) h += '<div class="hint" style="margin-top:8px">报告已保存: '+esc(j.savedTo)+'</div>';
    show("planOut", h);

    const hasBlockers = !!(r.blockers && r.blockers.length);
    const hasChanges = r.summary.changes > 0 && !hasBlockers;
    document.getElementById("confirmRemoval").disabled = r.counts.remove === 0;
    updateCleanupOptions();
    document.getElementById("applyReady").classList.toggle("hidden", !hasChanges);
    document.getElementById("applyReady").dataset.hasRemoves = r.counts.remove > 0 ? "1" : "0";
    document.getElementById("applyBlocked").classList.toggle("hidden", hasChanges);
    if(hasBlockers){
      document.getElementById("applyBlocked").textContent = "预览存在阻止项，请先解决后重新预览。";
    }
    if(!r.summary.changes){
      document.getElementById("applyBlocked").textContent = "本次预览为零差异：云端已与本地一致，无需执行。";
      document.getElementById("applyBlocked").classList.remove("hidden");
    }
  } finally { busy(btn, false); }
};

// ---------- apply ----------
document.getElementById("btnApply").onclick = async (ev) => {
  if(!currentPlan){show("applyOut", '<div class="banner warn">请先重新预览差异。</div>'); return;}
  const mode = document.getElementById("removalMode").value;
  const confirmRemoval = document.getElementById("confirmRemoval").checked;
  if(confirmRemoval && removalStats(currentPlan).exceeds && !document.getElementById("ackBlast").checked){
    show("applyOut", '<div class="banner warn">本次清理量较大，请核对上方数量并勾选大批量清理确认。</div>'); return;
  }
  const msg = "即将对云端执行对齐。\n\n" + document.getElementById("cleanupSummary").textContent
    + "\n\n本地目录不会被修改。确定继续?";
  if(!window.confirm(msg)) return;

  const btn = ev.target; busy(btn, true, "执行中…");
  currentPlan = null;
  // A lost response does not prove that the cloud was unchanged.
  document.getElementById("applyReady").classList.add("hidden");
  document.getElementById("applyBlocked").classList.remove("hidden");
  document.getElementById("applyBlocked").textContent = "本次执行计划已使用，请等待结果；再次执行前需要重新预览差异。";
  try{
    const j = await post("/api/apply", {
      removalMode: mode,
      quarantineRoot: document.getElementById("quarantineRoot").value.trim(),
      confirmRemoval: confirmRemoval,
      acknowledgeRecycle: mode === "recycle" && confirmRemoval,
      acknowledgeBlast: document.getElementById("ackBlast").checked,
      maxRemovePercent: 10,
      maxRemoveBytes: 10*1024*1024*1024
    });
    if(!j.ok){ show("applyOut", '<div class="banner warn">'+esc(j.error)+'</div>'); return; }
    const r = j.report;
    let h = "<div class='tally'>"
      + "<div><b>"+r.mkdirs.length+"</b>建目录</div>"
      + "<div><b>"+r.uploads.filter(x=>x.OK).length+"</b>上传成功</div>"
      + "<div><b>"+r.removes.filter(x=>x.OK).length+"</b>清理成功</div>"
      + "</div>";
    h += "<div class='hint'>清理方式: "+esc(r.removalMode)
      + (r.removalMode==="quarantine" ? "（隔离区 "+esc(r.quarantineRoot)+"/"+esc(r.runId)+"）" : "")
      + "；耗时 "+esc(r.startedAt)+" → "+esc(r.finishedAt)+"</div>";
    if(r.removalSkipped) h += '<div class="banner warn"><b>清理未执行：</b>'+esc(r.removalReason)+'</div>';
    if(r.aborted) h += '<div class="banner warn"><b>已中止：</b>'+esc(r.abortReason)+'</div>';
    const problems = [].concat(r.mkdirs, r.uploads, r.removes).filter(x => !x.OK);
    if(problems.length){
      h += "<h3 style='font-size:14px'>未完成 / 跳过</h3><table><tr><th>路径</th><th>说明</th></tr>";
      problems.forEach(p => h += "<tr><td>"+esc(p.Action.RelPath||p.Action.RemotePath)+"</td><td>"
        + esc(p.Error||p.Detail||"")+"</td></tr>");
      h += "</table>";
    }
    if(r.recoverySteps && r.recoverySteps.length){
      h += "<h3 style='font-size:14px'>恢复 / 后续</h3><ul style='margin:0;padding-left:20px'>";
      r.recoverySteps.forEach(s => h += "<li>"+esc(s)+"</li>");
      h += "</ul>";
    }
    h += r.succeeded ? "<div class='banner'><b>完成</b> — 请再点一次「预览差异」确认零差异。</div>"
                     : "<div class='banner warn'>存在未完成项，请查看上方清单并按提示重试。</div>";
    if(j.savedTo) h += '<div class="hint">执行记录已保存: '+esc(j.savedTo)+'</div>';
    show("applyOut", h);
    // The plan is spent; force a fresh preview before another apply.
    document.getElementById("applyReady").classList.add("hidden");
    document.getElementById("applyBlocked").classList.remove("hidden");
    document.getElementById("applyBlocked").textContent = "执行已完成。请重新「预览差异」后再决定是否再次执行。";
  } catch(e){
    show("applyOut", '<div class="banner warn">结果显示失败，请查看日志和执行记录，并重新预览差异。'+esc(e.message)+'</div>');
  } finally { busy(btn, false); refreshLog(); }
};

// ---------- log ----------
async function refreshLog(){
  try{
    const j = await request("/api/log");
    if(j.ok){
      const el = document.getElementById("log");
      el.textContent = j.lines.length ? j.lines.join("\n") : "（暂无日志）";
      el.scrollTop = el.scrollHeight;
    }
  }catch(e){}
}
document.getElementById("btnLog").onclick = refreshLog;
document.getElementById("btnQuit").onclick = async () => {
  if(!window.confirm("关闭这个本地界面? （不会影响网盘上的任何内容）")) return;
  try{ await post("/api/quit"); }catch(e){}
  document.body.innerHTML = "<div style='padding:40px;text-align:center;font-family:sans-serif'>"
    + "<h2>界面已关闭</h2><p>可以关闭此标签页。网盘内容不受影响。</p></div>";
};

refreshAccount();
refreshLog();
setInterval(refreshLog, 4000);
</script>
</body>
</html>
`
