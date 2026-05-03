const socket = io();
let pipelines = [];
let tasks = [];
let selectedTaskId = null;

document.addEventListener('DOMContentLoaded', () => {
  setupEventListeners();
});

function setupEventListeners() {
  document.getElementById('addPipelineBtn').addEventListener('click', () => {
    document.getElementById('pipelineModal').classList.add('show');
  });

  document.querySelector('.close-btn').addEventListener('click', () => {
    document.getElementById('pipelineModal').classList.remove('show');
  });

  document.getElementById('pipelineForm').addEventListener('submit', handleFormSubmit);

  socket.on('initialData', (data) => {
    pipelines = data.pipelines;
    tasks = data.tasks;
    renderPipelines();
    renderTasks();
  });

  socket.on('taskUpdate', (task) => {
    const index = tasks.findIndex(t => t.id === task.id);
    if (index !== -1) {
      tasks[index] = task;
    } else {
      tasks.unshift(task);
    }
    renderTasks();
    if (selectedTaskId === task.id) {
      renderLogs(task);
    }
  });
}

async function handleFormSubmit(e) {
  e.preventDefault();
  
  const name = document.getElementById('pipelineName').value;
  const repo = document.getElementById('pipelineRepo').value;
  const branch = document.getElementById('pipelineBranch').value;
  const commands = document.getElementById('pipelineCommands').value.split('\n').filter(c => c.trim());
  const cache = document.getElementById('pipelineCache').value.split('\n').filter(c => c.trim());
  const timeout = parseInt(document.getElementById('pipelineTimeout').value);
  const plugins = document.getElementById('pipelinePlugins').value.split('\n').filter(c => c.trim());

  const response = await fetch('/api/pipelines', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name, repo, branch, commands, cache, timeout, plugins })
  });

  const pipeline = await response.json();
  pipelines.push(pipeline);
  renderPipelines();
  document.getElementById('pipelineModal').classList.remove('show');
  document.getElementById('pipelineForm').reset();
}

async function triggerPipeline(id) {
  const response = await fetch(`/api/pipelines/${id}/trigger`, {
    method: 'POST'
  });
  const task = await response.json();
  tasks.unshift(task);
  renderTasks();
}

async function deletePipeline(id) {
  if (!confirm('确定删除此流水线？')) return;
  await fetch(`/api/pipelines/${id}`, { method: 'DELETE' });
  pipelines = pipelines.filter(p => p.id !== id);
  renderPipelines();
}

function selectTask(id) {
  selectedTaskId = id;
  const task = tasks.find(t => t.id === id);
  if (task) {
    renderLogs(task);
  }
}

function renderPipelines() {
  const list = document.getElementById('pipelineList');
  list.innerHTML = pipelines.map(p => `
    <div class="pipeline-item">
      <h3>${escapeHtml(p.name)}</h3>
      <p>仓库: ${escapeHtml(p.repo)} | 分支: ${escapeHtml(p.branch)}</p>
      <div class="pipeline-actions">
        <button class="btn btn-success btn-sm" onclick="triggerPipeline('${p.id}')">触发构建</button>
        <button class="btn btn-danger btn-sm" onclick="deletePipeline('${p.id}')">删除</button>
      </div>
    </div>
  `).join('');
}

function renderTasks() {
  const list = document.getElementById('taskList');
  list.innerHTML = tasks.map(t => `
    <div class="task-item" onclick="selectTask('${t.id}')" style="cursor: pointer;">
      <h3>${escapeHtml(t.pipelineName)}</h3>
      <p>状态: <span class="status status-${t.status}">${t.status}</span></p>
      <p>时间: ${new Date(t.createdAt).toLocaleString()}</p>
    </div>
  `).join('');
}

function renderLogs(task) {
  const content = document.getElementById('logContent');
  content.textContent = task.logs.join('\n') || '暂无日志...';
  const viewer = document.getElementById('logViewer');
  viewer.scrollTop = viewer.scrollHeight;
}

function escapeHtml(text) {
  const div = document.createElement('div');
  div.textContent = text;
  return div.innerHTML;
}
