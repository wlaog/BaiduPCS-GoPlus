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
.k-RENAME{color:var(--add);font-weight:700}
.k-WAIT{color:var(--warn);font-weight:700}
.k-REMOVE{color:var(--del);font-weight:700}
.k-KEEP{color:var(--muted);font-weight:700}
.k-CONFLICT{color:var(--del);font-weight:700}
.k-MKDIR{color:var(--accent);font-weight:700}
pre#log{background:#0d1117;color:#c9d1d9;padding:12px;border-radius:8px;max-height:260px;
  overflow:auto;font-size:12px;line-height:1.5;white-space:pre-wrap;margin:0}
.progress-heading{display:flex;justify-content:space-between;gap:12px;align-items:center}
.progress-heading h2{margin:0}.progress-meta{font-size:12px;color:var(--muted);margin:8px 0;overflow-wrap:anywhere}
progress{width:100%;height:10px;accent-color:var(--accent);display:block;margin:12px 0}
.live-log{font:12px/1.65 Consolas,"Microsoft YaHei",monospace;white-space:pre-wrap;overflow-wrap:anywhere;color:var(--muted);margin:12px 0 0;max-height:145px;overflow:auto}
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

<p class="intro">仅清理和重命名云端文件，上传与新建目录由百度网盘自动备份完成。</p>

<section class="card hidden" id="operationProgress" aria-label="操作进度">
  <div class="progress-heading"><h2 id="progressTitle" role="status">等待开始</h2><span class="hint" id="progressElapsed"></span></div>
  <progress id="progressBar" max="100" aria-label="当前阶段进度"></progress>
  <div class="progress-meta" id="progressMeta"></div>
  <pre class="live-log" id="liveLog" aria-label="最近运行日志"></pre>
</section>

<details class="card help" id="usageGuide"><summary>使用说明与恢复方法</summary><div class="details-body">
<ol class="steps"><li><b>先完成官方自动备份</b>本工具不上传、不覆盖内容、不新建目录。云端没有最新内容时，会显示 WAIT 并保留相关历史版本。</li>
<li><b>预览并核对目录</b>RENAME 按内容匹配正确版本，REMOVE 清理本地已不存在的内容；本地真实存在的带括号文件名会保留。</li>
<li><b>确认后整理</b>删除进入网盘回收站。失败项跳过，其他独立项继续；对应正确版本尚未就位时保留其历史副本。</li>
<li><b>重试与恢复</b>保持启动窗口运行，可仅重试未完成项，刷新页面也能恢复待重试计划。关闭服务后需要重新预览。回收站期限以网盘为准。</li></ol>
<p class="hint">删除和重命名仍可能与官方任务竞争，建议等该备份任务空闲或暂停后整理。程序会复核每项状态，但不提供服务端锁。</p>
</div></details>

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
    <label class="check"><input type="checkbox" id="hashFiles" checked>重新核对完整内容（推荐）</label>
    <p class="field-note">取消后使用快速校验：复用大小和修改时间未变的文件哈希，减少读盘；可能漏掉保留原大小和时间的内容修改。</p>
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
    <div class="hint">删除进入网盘回收站，恢复期限以网盘实际显示为准。失败项会保留并继续其他独立项。</div>
    <div style="margin-top:10px">
      <label class="check"><input type="checkbox" id="confirmRemoval">我已核对预览，确认执行本次删除与重命名</label>
    </div>
    <div id="blastWarning" class="banner warn hidden">
      <strong>清理较多或包含未展开目录，请核对范围</strong>
      <p id="blastSummary"></p>
      <p>清理超过文件数 10% 或 10 GB，或整目录未展开、无法统计内部数量时，需要额外确认。整目录回收包含其全部内容，包括内部临时文件。</p>
      <label class="check"><input type="checkbox" id="ackBlast">我已核对清理范围，确认包括所列目录的全部内容</label>
    </div>
    <div class="actions">
      <button class="danger" id="btnApply">执行整理</button>
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
  catch(e){ throw new Error("与本地服务的连接已中断。操作可能已部分完成，请检查启动窗口和运行日志；连接恢复且操作结束后刷新页面恢复剩余计划，不要直接重复执行。"); }
  let j;
  try{ j = await r.json(); }
  catch(e){ throw new Error("本地服务未返回有效结果（HTTP "+r.status+"），请查看启动窗口，待操作结束后刷新页面恢复剩余计划。"); }
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
let busyControls = [];
function busy(btn, on, label){
  if(on){
    busyControls = Array.from(document.querySelectorAll("button,input,select")).filter(el=>el.id!=="btnLog");
    busyControls.forEach(el=>{el.dataset.beforeBusy=el.disabled?"1":"0";el.disabled=true;});
    document.getElementById("operationProgress").classList.remove("hidden");
    document.getElementById("progressTitle").textContent=label||"准备开始…";
    document.getElementById("progressMeta").textContent="正在准备，请稍候…";
    document.getElementById("progressBar").removeAttribute("value");
    document.getElementById("progressBar").classList.remove("hidden");
  }else{
    busyControls.forEach(el=>{el.disabled=el.dataset.beforeBusy==="1";delete el.dataset.beforeBusy;});busyControls=[];
    refreshLog();
  }
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
for(const id of ["localDir", "remoteRoot", "hashFiles"]){
  document.getElementById(id).addEventListener("input", invalidatePlan);
}
function removalStats(report){
  const s = report.summary;
  const percent = s.cloudFiles > 0 ? s.removeFiles / s.cloudFiles * 100 : 0;
  return {percent, exceeds: (s.unscannedRemoveDirs||0)>0 || percent > 10 || s.removeBytes > 10*1024*1024*1024};
}
function updateCleanupOptions(){
  if(!currentPlan)return;
  const s=currentPlan.summary,risk=removalStats(currentPlan);
  const opaque=s.unscannedRemoveDirs||0;
  document.getElementById("cleanupSummary").textContent="重命名 "+currentPlan.counts.rename+" 项；回收已统计文件 "+s.removeFiles+" 个（含重命名前同名旧版，"+size(s.removeBytes)+"）"+(opaque?"；另整目录回收 "+opaque+" 项及其全部内容，内部数量与容量未统计":"")+"。等待备份与需判断的项将保留。";
  document.getElementById("blastWarning").classList.toggle("hidden",!risk.exceeds);
  document.getElementById("blastSummary").textContent=opaque?"另有 "+opaque+" 个目录将连同全部内容一次回收；未扫描内部文件，无法判断其是否超过 10% 或 10 GB。":"拟清理 "+s.removeFiles+" 个文件，占云端 "+risk.percent.toFixed(1)+"%，共 "+size(s.removeBytes)+"。";
  if(!busyControls.length)document.getElementById("btnApply").disabled=!document.getElementById("confirmRemoval").checked;
}
document.getElementById("confirmRemoval").onchange=updateCleanupOptions;
document.getElementById("ackBlast").onchange=updateCleanupOptions;

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
let initialPlanRestore=true;
async function refreshAccount(){
  const b = document.getElementById("badgeAccount");
  try{
    const j = await request("/api/state");
    if(initialPlanRestore){
      initialPlanRestore=false;
      if(j.pendingPlan){
        document.getElementById("localDir").value=j.pendingPlan.localRoot;
        document.getElementById("remoteRoot").value=j.pendingPlan.remoteRoot;
        renderPlan(j.pendingPlan);
        document.getElementById("btnApply").textContent="执行剩余整理项";
      }
    }
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

// ---------- cleanup preview ----------
function renderPlan(r, savedTo){
  currentPlan=r;
  let h="<div class='tally'>"
    +"<div><b class='k-RENAME'>"+r.counts.rename+"</b>RENAME 重命名</div>"
    +"<div><b class='k-REMOVE'>"+r.counts.remove+"</b>REMOVE 多余项</div>"
    +"<div><b class='k-KEEP'>"+r.counts.keep+"</b>KEEP 已一致</div>"
    +"<div><b class='k-WAIT'>"+r.counts.pending+"</b>WAIT 等待备份</div>"
    +"<div><b class='k-CONFLICT'>"+r.counts.conflict+"</b>跳过 / 需判断</div></div>";
  h+="<div class='hint'>本地 "+r.summary.localFiles+" 文件 / 云端已扫描 "+r.summary.cloudFiles
    +" 文件；拟回收已统计文件 "+r.summary.removeFiles+" 个（含重命名前同名旧版），共 "+size(r.summary.removeBytes)+"。</div>";
  if(r.summary.unscannedRemoveDirs)h+="<div class='banner warn'>另有 "+r.summary.unscannedRemoveDirs+" 个目录整棵回收（包含全部内容和内部临时文件）；未向下扫描，内部文件数量与容量不计入上述统计。</div>";
  if(r.performance)h+="<div class='hint'>内容读取 "+size(r.performance.hashBytes)+"；复用 "+r.performance.cacheHits+" 个哈希缓存；云端目录请求 "+r.performance.cloudListings+" 次。</div>";
  function section(title,note,items){
    if(!items?.length)return "";
    let s="<h3 style='font-size:14px;margin-top:14px'>"+title+" ("+items.length+")</h3><div class='hint'>"+note+"</div>";
    s+="<div class='table-wrap'><table><tr><th>相对路径</th><th>说明</th></tr>";
    items.slice(0,300).forEach(a=>{
      s+="<tr><td>"+esc(a.relPath)+(a.isDir?"/":"")+"</td><td>"+esc(a.reason);
      if(a.sourcePath)s+="<div class='hint'>来源："+esc(a.sourcePath)+"</div>";
      s+="</td></tr>";
    });
    if(items.length>300)s+="<tr><td colspan=2>其余 "+(items.length-300)+" 项见报告</td></tr>";
    return s+"</table></div>";
  }
  h+=section("RENAME 重命名为本地名称","已按内容确认版本；存在同名旧版时，先移入回收站。",r.renames);
  h+=section("REMOVE 云端多余项","本地不存在的最外层目录直接整棵回收；单项失败跳过，其他独立项继续。",r.removes);
  h+=section("WAIT 等待官方备份","尚未找到本地最新内容，保留相关历史版本。",r.pending);
  h+=section("跳过 / 需要判断","只跳过相关文件，不阻止其他独立项。",r.conflicts);
  if(r.blockers?.length)h+='<div class="banner warn"><b>无法执行：</b>'+r.blockers.map(esc).join("；")+'</div>';
  if(savedTo)h+='<div class="hint">报告已保存：'+esc(savedTo)+'</div>';
  show("planOut",h);
  const actionable=r.summary.changes>0&&!r.blockers?.length;
  document.getElementById("applyReady").classList.toggle("hidden",!actionable);
  const blocked=document.getElementById("applyBlocked");blocked.classList.toggle("hidden",actionable);
  if(!actionable)blocked.textContent=r.blockers?.length?"请先解决扫描完整性等阻止项。":
    r.summary.zeroDifference?"云端已与本地一致，无需整理。":"暂无可执行动作；请等待官方备份完成，或处理上方需判断项。";
  updateCleanupOptions();
}
document.getElementById("btnPlan").onclick=async(ev)=>{
  invalidatePlan();const epoch=planEpoch;const btn=ev.target;busy(btn,true,"扫描中…");
  document.getElementById("btnApply").textContent="执行整理";
  try{
    const j=await post("/api/plan",{localDir:document.getElementById("localDir").value.trim(),remoteRoot:document.getElementById("remoteRoot").value.trim(),hashFiles:document.getElementById("hashFiles").checked});
    if(epoch!==planEpoch)return;
    if(!j.ok){show("planOut",'<div class="banner warn">'+esc(j.error)+'</div>');return;}
    renderPlan(j.report,j.savedTo);
  }finally{busy(btn,false);updateCleanupOptions();}
};

// ---------- cleanup execution / retry only unfinished actions ----------
document.getElementById("btnApply").onclick=async(ev)=>{
  if(!currentPlan)return;
  if(!document.getElementById("confirmRemoval").checked){show("applyOut",'<div class="banner warn">请先核对预览并确认执行本次整理。</div>');return;}
  if(removalStats(currentPlan).exceeds&&!document.getElementById("ackBlast").checked){show("applyOut",'<div class="banner warn">请核对大批量清理数量并勾选额外确认。</div>');return;}
  if(!window.confirm(document.getElementById("cleanupSummary").textContent+"\n\n仅删除和重命名云端文件，本地文件不会改变。确认继续？"))return;
  const btn=ev.target;busy(btn,true,"整理中…");currentPlan=null;
  document.getElementById("applyReady").classList.add("hidden");
  document.getElementById("applyBlocked").classList.remove("hidden");
  document.getElementById("applyBlocked").textContent="正在逐项处理，单项失败会跳过并继续。";
  try{
    const j=await post("/api/apply",{removalMode:"recycle",confirmRemoval:true,acknowledgeRecycle:true,acknowledgeBlast:document.getElementById("ackBlast").checked,maxRemovePercent:10,maxRemoveBytes:10*1024*1024*1024});
    if(!j.ok){if(j.remainingReport)renderPlan(j.remainingReport);show("applyOut",'<div class="banner warn">'+esc(j.error)+'</div>');return;}
    const r=j.report,renames=r.renames||[],removes=r.removes||[];
    let h="<div class='tally'><div><b>"+renames.filter(x=>x.OK).length+"</b>重命名成功</div>"
      +"<div><b>"+(removes.filter(x=>x.OK).length+renames.filter(x=>x.TargetRemoved).length)+"</b>清理成功（项）</div>"
      +"<div><b>"+r.failures+"</b>未完成 / 跳过</div></div>";
    h+="<div class='hint'>"+esc(r.startedAt)+" → "+esc(r.finishedAt)+"；等待官方备份 "+r.pendingCount+" 项。</div>";
    if(r.removalSkipped)h+='<div class="banner warn">'+esc(r.removalReason)+'</div>';
    if(r.aborted)h+='<div class="banner warn">'+esc(r.abortReason)+'</div>';
    const problems=[...renames,...removes].filter(x=>!x.OK);
    if(problems.length){h+="<div class='table-wrap'><table><tr><th>未完成项</th><th>说明</th></tr>";problems.forEach(p=>h+="<tr><td>"+esc(p.Action.RelPath)+"</td><td>"+esc(p.Error||p.Detail)+"</td></tr>");h+="</table></div>";}
    if(r.recoverySteps?.length)h+="<div class='hint'>"+r.recoverySteps.map(esc).join("<br>")+"</div>";
    if(j.remainingReport){
      renderPlan(j.remainingReport);
      h+='<div class="banner warn">其他独立项已处理。现在可仅重试未完成项，无需重新全盘扫描；重试前仍会逐项复核。</div>';
    }else{
      h+='<div class="banner">本次可执行项已完成。等待备份和需判断项仍保留；不会上传或创建目录。</div>';
      document.getElementById("applyReady").classList.add("hidden");
      document.getElementById("applyBlocked").textContent="本次可执行项已完成。新的云端变化需要重新预览后才会处理。";
    }
    if(j.savedTo)h+='<div class="hint">执行记录：'+esc(j.savedTo)+'</div>';
    show("applyOut",h);
  }finally{
    busy(btn,false);
    document.getElementById("btnApply").textContent=currentPlan?"仅重试未完成项":"执行整理";
    updateCleanupOptions();refreshLog();
  }
};

// ---------- progress and log ----------
const phaseLabels={rename:"重命名云端版本",starting:"准备开始",local:"扫描本地",cloud:"扫描云端",hash:"核对文件内容",
  verify:"核验写入结果",remove:"清理云端多余项",planned:"预览完成",recheck:"执行前复核","recheck-remove":"清理前复核"};
function renderProgress(p){
  if(!p || !p.startedAt) return;
  document.getElementById("operationProgress").classList.remove("hidden");
  const title=document.getElementById("progressTitle"),bar=document.getElementById("progressBar");
  bar.classList.toggle("hidden",!p.running && p.status!=="complete");
  title.textContent=p.running?(phaseLabels[p.phase]||"处理中"):
    p.status==="error"?"操作失败，请查看日志":p.status==="warning"?"操作结束，存在未完成项":"操作完成";
  if(p.running) document.getElementById("progressElapsed").textContent="已用时 "+Math.max(0,Math.floor((Date.now()-p.startedAt)/1000))+" 秒";
  const bytePhase=p.phase==="transfer" || p.phase==="hash-upload";
  let info=[];
  if(p.total>0){
    const done=bytePhase?(p.bytes||0):p.done;
    bar.value=Math.min(100,Math.max(0,done/p.total*100));
    info.push(bytePhase?"当前文件 "+size(done)+" / "+size(p.total):"当前阶段 "+done+" / "+p.total+" 项");
  }else{bar.removeAttribute("value");if(p.running)info.push("正在统计，总量尚未确定");}
  if(!p.running){if(p.status==="complete")bar.value=100;else bar.removeAttribute("value");}
  if(p.files || p.dirs) info.push("已发现 "+(p.files||0)+" 文件、"+(p.dirs||0)+" 目录");
  if(p.bytes && !bytePhase)info.push((p.phase==="hash"?"已读取 ":"已统计 ")+size(p.bytes));
  if(p.current)info.push(p.current);
  document.getElementById("progressMeta").textContent=info.join(" · ");
}
let logFetching=false;
async function refreshLog(){
  if(logFetching)return;logFetching=true;
  try{
    const j = await request("/api/log");
    if(j.ok){
      const el = document.getElementById("log");
      const follow=el.scrollTop+el.clientHeight>=el.scrollHeight-30;
      el.textContent = j.lines.length ? j.lines.join("\n") : "（暂无日志）";
      if(follow)el.scrollTop = el.scrollHeight;
      const live=document.getElementById("liveLog");live.textContent=j.lines.slice(-6).join("\n");live.scrollTop=live.scrollHeight;
      renderProgress(j.progress);
    }
  }catch(e){
    if(busyControls.length){document.getElementById("progressMeta").textContent="进度连接暂时中断，正在重连；请保留启动窗口并等待操作结果。";}
  }finally{logFetching=false;}
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
setInterval(refreshLog, 800);
</script>
</body>
</html>
`
