const express = require('express');
const http = require('http');
const { Server } = require('socket.io');
const cors = require('cors');
const bodyParser = require('body-parser');
const path = require('path');
const fs = require('fs');
const axios = require('axios');

const app = express();
const server = http.createServer(app);
const io = new Server(server, {
  cors: {
    origin: '*',
    methods: ['GET', 'POST']
  }
});

const PORT = 3000;
const GO_BUILDER_URL = 'http://localhost:8080';
const DATA_DIR = path.join(__dirname, '../data');

if (!fs.existsSync(DATA_DIR)) {
  fs.mkdirSync(DATA_DIR);
}

const CONFIG_FILE = path.join(DATA_DIR, 'pipelines.json');
const TASKS_FILE = path.join(DATA_DIR, 'tasks.json');

let pipelines = [];
let tasks = [];

function loadData() {
  if (fs.existsSync(CONFIG_FILE)) {
    pipelines = JSON.parse(fs.readFileSync(CONFIG_FILE, 'utf8'));
  }
  if (fs.existsSync(TASKS_FILE)) {
    tasks = JSON.parse(fs.readFileSync(TASKS_FILE, 'utf8'));
  }
}

function saveData() {
  fs.writeFileSync(CONFIG_FILE, JSON.stringify(pipelines, null, 2));
  fs.writeFileSync(TASKS_FILE, JSON.stringify(tasks, null, 2));
}

loadData();

app.use(cors());
app.use(bodyParser.json());
app.use(express.static(path.join(__dirname, 'public')));

app.get('/api/pipelines', (req, res) => {
  res.json(pipelines);
});

app.post('/api/pipelines', (req, res) => {
  const pipeline = {
    id: Date.now().toString(),
    name: req.body.name,
    repo: req.body.repo,
    branch: req.body.branch || 'main',
    commands: req.body.commands || [],
    cache: req.body.cache || [],
    timeout: req.body.timeout || 600,
    plugins: req.body.plugins || [],
    createdAt: new Date().toISOString()
  };
  pipelines.push(pipeline);
  saveData();
  res.json(pipeline);
});

app.delete('/api/pipelines/:id', (req, res) => {
  pipelines = pipelines.filter(p => p.id !== req.params.id);
  saveData();
  res.json({ success: true });
});

app.get('/api/tasks', (req, res) => {
  res.json(tasks);
});

app.post('/api/pipelines/:id/trigger', async (req, res) => {
  const pipeline = pipelines.find(p => p.id === req.params.id);
  if (!pipeline) {
    return res.status(404).json({ error: 'Pipeline not found' });
  }

  const task = {
    id: Date.now().toString(),
    pipelineId: pipeline.id,
    pipelineName: pipeline.name,
    status: 'pending',
    logs: [],
    createdAt: new Date().toISOString(),
    startedAt: null,
    finishedAt: null
  };
  tasks.unshift(task);
  saveData();

  try {
    await axios.post(`${GO_BUILDER_URL}/build`, {
      taskId: task.id,
      repo: pipeline.repo,
      branch: pipeline.branch,
      commands: pipeline.commands,
      cache: pipeline.cache,
      timeout: pipeline.timeout,
      plugins: pipeline.plugins
    });
  } catch (error) {
    task.status = 'failed';
    task.logs.push(`Error: Failed to connect to builder - ${error.message}`);
    saveData();
    io.emit('taskUpdate', task);
  }

  res.json(task);
});

app.post('/webhook/github', (req, res) => {
  const event = req.headers['x-github-event'];
  const payload = req.body;
  
  if (event === 'push') {
    const repo = payload.repository?.html_url;
    const branch = payload.ref?.replace('refs/heads/', '');
    
    const matchingPipelines = pipelines.filter(p => 
      p.repo.includes(repo?.replace('https://github.com/', '')) && 
      (p.branch === branch || p.branch === '*')
    );
    
    matchingPipelines.forEach(async (pipeline) => {
      const task = {
        id: Date.now().toString() + Math.random().toString(36).substr(2, 9),
        pipelineId: pipeline.id,
        pipelineName: pipeline.name,
        status: 'pending',
        logs: [],
        createdAt: new Date().toISOString(),
        startedAt: null,
        finishedAt: null
      };
      tasks.unshift(task);
      saveData();
      
      try {
        await axios.post(`${GO_BUILDER_URL}/build`, {
          taskId: task.id,
          repo: pipeline.repo,
          branch: pipeline.branch,
          commands: pipeline.commands,
          cache: pipeline.cache,
          timeout: pipeline.timeout,
          plugins: pipeline.plugins
        });
      } catch (error) {
        console.error('Failed to trigger build:', error);
      }
    });
  }
  
  res.json({ success: true });
});

app.post('/webhook/gitlab', (req, res) => {
  const event = req.headers['x-gitlab-event'];
  const payload = req.body;
  
  if (event === 'Push Hook') {
    const repo = payload.project?.web_url;
    const branch = payload.ref?.replace('refs/heads/', '');
    
    const matchingPipelines = pipelines.filter(p => 
      p.repo.includes(repo?.replace('https://gitlab.com/', '')) && 
      (p.branch === branch || p.branch === '*')
    );
    
    matchingPipelines.forEach(async (pipeline) => {
      const task = {
        id: Date.now().toString() + Math.random().toString(36).substr(2, 9),
        pipelineId: pipeline.id,
        pipelineName: pipeline.name,
        status: 'pending',
        logs: [],
        createdAt: new Date().toISOString(),
        startedAt: null,
        finishedAt: null
      };
      tasks.unshift(task);
      saveData();
      
      try {
        await axios.post(`${GO_BUILDER_URL}/build`, {
          taskId: task.id,
          repo: pipeline.repo,
          branch: pipeline.branch,
          commands: pipeline.commands,
          cache: pipeline.cache,
          timeout: pipeline.timeout,
          plugins: pipeline.plugins
        });
      } catch (error) {
        console.error('Failed to trigger build:', error);
      }
    });
  }
  
  res.json({ success: true });
});

app.post('/api/tasks/:id/logs', (req, res) => {
  const task = tasks.find(t => t.id === req.params.id);
  if (!task) {
    return res.status(404).json({ error: 'Task not found' });
  }
  
  if (req.body.log) {
    task.logs.push(req.body.log);
  }
  if (req.body.status) {
    task.status = req.body.status;
    if (req.body.status === 'running' && !task.startedAt) {
      task.startedAt = new Date().toISOString();
    }
    if (['success', 'failed'].includes(req.body.status) && !task.finishedAt) {
      task.finishedAt = new Date().toISOString();
    }
  }
  
  saveData();
  io.emit('taskUpdate', task);
  res.json({ success: true });
});

io.on('connection', (socket) => {
  console.log('Client connected');
  socket.emit('initialData', { pipelines, tasks });
});

server.listen(PORT, () => {
  console.log(`Node.js 服务运行在 http://localhost:${PORT}`);
});
