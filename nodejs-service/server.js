const express = require('express');
const http = require('http');
const { Server } = require('socket.io');
const cors = require('cors');
const bodyParser = require('body-parser');
const path = require('path');
const fs = require('fs');
const axios = require('axios');
const Redis = require('ioredis');
const schedule = require('node-schedule');

const app = express();
const server = http.createServer(app);
const io = new Server(server, {
  cors: {
    origin: '*',
    methods: ['GET', 'POST']
  }
});

const PORT = 3000;
const REDIS_HOST = process.env.REDIS_HOST || 'localhost';
const REDIS_PORT = process.env.REDIS_PORT || 6379;
const DATA_DIR = path.join(__dirname, '../data');

const REDIS_QUEUE_MAIN = 'ci:tasks:main';
const REDIS_QUEUE_PROCESSING = 'ci:tasks:processing';
const REDIS_TASKS_HASH = 'ci:tasks:hash';
const REDIS_METRICS = 'ci:metrics';
const MAX_QUEUE_SIZE = 10000;
const TASK_TIMEOUT_SECONDS = 3600;

let redis;
let redisSub;

function createRedisClient() {
  const client = new Redis({
    host: REDIS_HOST,
    port: REDIS_PORT,
    maxRetriesPerRequest: null,
    enableReadyCheck: false
  });

  client.on('error', (err) => {
    console.error('Redis connection error:', err);
  });

  client.on('connect', () => {
    console.log('Connected to Redis');
  });

  return client;
}

redis = createRedisClient();
redisSub = createRedisClient();

if (!fs.existsSync(DATA_DIR)) {
  fs.mkdirSync(DATA_DIR);
}

const CONFIG_FILE = path.join(DATA_DIR, 'pipelines.json');

let pipelines = [];

function loadPipelines() {
  if (fs.existsSync(CONFIG_FILE)) {
    pipelines = JSON.parse(fs.readFileSync(CONFIG_FILE, 'utf8'));
  }
}

function savePipelines() {
  fs.writeFileSync(CONFIG_FILE, JSON.stringify(pipelines, null, 2));
}

loadPipelines();

app.use(cors());
app.use(bodyParser.json());
app.use(express.static(path.join(__dirname, 'public')));

async function initializeTaskInRedis(task) {
  const taskKey = `task:${task.id}`;

  const taskData = {
    id: task.id,
    pipelineId: task.pipelineId,
    pipelineName: task.pipelineName,
    status: 'pending',
    logs: [],
    createdAt: task.createdAt,
    startedAt: null,
    finishedAt: null,
    retryCount: 0,
    maxRetries: task.maxRetries || 3
  };

  const multi = redis.multi();
  multi.hset(taskKey, Object.entries(taskData).reduce((acc, [k, v]) => {
    acc[k] = typeof v === 'object' ? JSON.stringify(v) : String(v || '');
    return acc;
  }, {}));
  multi.lpush(REDIS_QUEUE_MAIN, task.id);
  multi.incr(REDIS_METRICS);
  await multi.exec();

  return taskData;
}

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
    maxRetries: req.body.maxRetries || 3,
    createdAt: new Date().toISOString()
  };
  pipelines.push(pipeline);
  savePipelines();
  res.json(pipeline);
});

app.delete('/api/pipelines/:id', (req, res) => {
  pipelines = pipelines.filter(p => p.id !== req.params.id);
  savePipelines();
  res.json({ success: true });
});

app.get('/api/tasks', async (req, res) => {
  try {
    const taskKeys = await redis.keys('task:*');
    const tasks = [];
    const validTaskKeys = taskKeys.filter(k => !k.includes(':details:') && !k.includes(':retry'));

    for (const taskKey of validTaskKeys) {
      const data = await redis.hgetall(taskKey);
      if (data && Object.keys(data).length > 0) {
        tasks.push(deserializeTask(data));
      }
    }

    tasks.sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt));
    res.json(tasks.slice(0, 100));
  } catch (error) {
    console.error('Error fetching tasks:', error);
    res.status(500).json({ error: 'Failed to fetch tasks' });
  }
});

app.post('/api/pipelines/:id/trigger', async (req, res) => {
  const pipeline = pipelines.find(p => p.id === req.params.id);
  if (!pipeline) {
    return res.status(404).json({ error: 'Pipeline not found' });
  }

  const queueSize = await redis.llen(REDIS_QUEUE_MAIN);
  if (queueSize >= MAX_QUEUE_SIZE) {
    return res.status(503).json({
      error: 'Queue is full. Backpressure activated.',
      queueSize,
      maxSize: MAX_QUEUE_SIZE
    });
  }

  const task = {
    id: Date.now().toString() + Math.random().toString(36).substr(2, 9),
    pipelineId: pipeline.id,
    pipelineName: pipeline.name,
    repo: pipeline.repo,
    branch: pipeline.branch,
    commands: pipeline.commands,
    cache: pipeline.cache,
    timeout: pipeline.timeout,
    plugins: pipeline.plugins,
    maxRetries: pipeline.maxRetries || 3,
    createdAt: new Date().toISOString()
  };

  try {
    const taskData = await initializeTaskInRedis(task);

    await redis.lpush(`${REDIS_QUEUE_MAIN}:details:${task.id}`, JSON.stringify({
      repo: pipeline.repo,
      branch: pipeline.branch,
      commands: pipeline.commands,
      cache: pipeline.cache,
      timeout: pipeline.timeout,
      plugins: pipeline.plugins
    }));

    io.emit('taskUpdate', taskData);
    res.json(taskData);
  } catch (error) {
    console.error('Error creating task:', error);
    res.status(500).json({ error: 'Failed to create task' });
  }
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
      const queueSize = await redis.llen(REDIS_QUEUE_MAIN);
      if (queueSize >= MAX_QUEUE_SIZE) {
        console.warn('Queue full, webhook task rejected');
        return;
      }

      const task = {
        id: Date.now().toString() + Math.random().toString(36).substr(2, 9),
        pipelineId: pipeline.id,
        pipelineName: pipeline.name,
        repo: pipeline.repo,
        branch: pipeline.branch,
        commands: pipeline.commands,
        cache: pipeline.cache,
        timeout: pipeline.timeout,
        plugins: pipeline.plugins,
        maxRetries: pipeline.maxRetries || 3,
        createdAt: new Date().toISOString()
      };

      try {
        const taskData = await initializeTaskInRedis(task);
        await redis.lpush(`${REDIS_QUEUE_MAIN}:details:${task.id}`, JSON.stringify({
          repo: pipeline.repo,
          branch: pipeline.branch,
          commands: pipeline.commands,
          cache: pipeline.cache,
          timeout: pipeline.timeout,
          plugins: pipeline.plugins
        }));
        io.emit('taskUpdate', taskData);
      } catch (error) {
        console.error('Error creating webhook task:', error);
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
      const queueSize = await redis.llen(REDIS_QUEUE_MAIN);
      if (queueSize >= MAX_QUEUE_SIZE) {
        console.warn('Queue full, webhook task rejected');
        return;
      }

      const task = {
        id: Date.now().toString() + Math.random().toString(36).substr(2, 9),
        pipelineId: pipeline.id,
        pipelineName: pipeline.name,
        repo: pipeline.repo,
        branch: pipeline.branch,
        commands: pipeline.commands,
        cache: pipeline.cache,
        timeout: pipeline.timeout,
        plugins: pipeline.plugins,
        maxRetries: pipeline.maxRetries || 3,
        createdAt: new Date().toISOString()
      };

      try {
        const taskData = await initializeTaskInRedis(task);
        await redis.lpush(`${REDIS_QUEUE_MAIN}:details:${task.id}`, JSON.stringify({
          repo: pipeline.repo,
          branch: pipeline.branch,
          commands: pipeline.commands,
          cache: pipeline.cache,
          timeout: pipeline.timeout,
          plugins: pipeline.plugins
        }));
        io.emit('taskUpdate', taskData);
      } catch (error) {
        console.error('Error creating webhook task:', error);
      }
    });
  }

  res.json({ success: true });
});

app.post('/api/tasks/:id/logs', async (req, res) => {
  const taskId = req.params.id;
  const taskKey = `task:${taskId}`;

  try {
    const exists = await redis.exists(taskKey);
    if (!exists) {
      return res.status(404).json({ error: 'Task not found' });
    }

    const multi = redis.multi();

    if (req.body.log) {
      multi.hincrby(taskKey, 'logCount', 1);
      const logCount = await redis.hincrby(taskKey, 'logCount', 0);
      multi.hset(taskKey, `log:${logCount}`, req.body.log);
    }

    if (req.body.status) {
      multi.hset(taskKey, 'status', req.body.status);

      if (req.body.status === 'running') {
        const existingStartedAt = await redis.hget(taskKey, 'startedAt');
        if (!existingStartedAt || existingStartedAt === '') {
          multi.hset(taskKey, 'startedAt', new Date().toISOString());
        }
      }

      if (['success', 'failed'].includes(req.body.status)) {
        multi.hset(taskKey, 'finishedAt', new Date().toISOString());
        multi.lrem(REDIS_QUEUE_PROCESSING, 0, taskId);
      }
    }

    if (typeof req.body.retryCount === 'number') {
      multi.hset(taskKey, 'retryCount', req.body.retryCount);
    }

    await multi.exec();

    const updatedTask = deserializeTask(await redis.hgetall(taskKey));
    io.emit('taskUpdate', updatedTask);
    res.json({ success: true });
  } catch (error) {
    console.error('Error updating task logs:', error);
    res.status(500).json({ error: 'Failed to update logs' });
  }
});

app.get('/api/queue/status', async (req, res) => {
  try {
    const mainQueueSize = await redis.llen(REDIS_QUEUE_MAIN);
    const processingSize = await redis.llen(REDIS_QUEUE_PROCESSING);
    const totalTasks = await redis.get(REDIS_METRICS) || 0;

    res.json({
      mainQueue: mainQueueSize,
      processing: processingSize,
      totalTasks: parseInt(totalTasks),
      maxQueueSize: MAX_QUEUE_SIZE,
      backpressureActive: mainQueueSize >= MAX_QUEUE_SIZE * 0.8
    });
  } catch (error) {
    res.status(500).json({ error: 'Failed to get queue status' });
  }
});

function deserializeTask(data) {
  const task = {};
  for (const [key, value] of Object.entries(data)) {
    if (key.startsWith('log:')) {
      if (!task.logs) task.logs = [];
      task.logs.push(value);
    } else if (key === 'logCount') {
      task[key] = parseInt(value);
    } else if (['retryCount', 'maxRetries'].includes(key)) {
      task[key] = parseInt(value) || 0;
    } else {
      task[key] = value;
    }
  }
  return task;
}

const periodicCheck = schedule.scheduleJob('*/30 * * * * *', async () => {
  try {
    const processingTasks = await redis.lrange(REDIS_QUEUE_PROCESSING, 0, -1);
    const now = Date.now();

    for (const taskId of processingTasks) {
      const taskKey = `task:${taskId}`;
      const startedAt = await redis.hget(taskKey, 'startedAt');

      if (startedAt && startedAt !== '') {
        const elapsed = (now - new Date(startedAt).getTime()) / 1000;
        const timeout = parseInt(await redis.hget(taskKey, 'timeout') || TASK_TIMEOUT_SECONDS);

        if (elapsed > timeout) {
          const retryCount = parseInt(await redis.hget(taskKey, 'retryCount') || '0');
          const maxRetries = parseInt(await redis.hget(taskKey, 'maxRetries') || '3');

          if (retryCount < maxRetries) {
            console.log(`Task ${taskId} timed out, moving back to queue (retry ${retryCount + 1})`);

            const details = await redis.lpop(`${REDIS_QUEUE_MAIN}:details:${taskId}`);
            await redis.multi()
              .hincrby(taskKey, 'retryCount', 1)
              .hset(taskKey, 'status', 'pending')
              .hset(taskKey, 'startedAt', '')
              .hset(taskKey, 'finishedAt', '')
              .lrem(REDIS_QUEUE_PROCESSING, 0, taskId)
              .lpush(REDIS_QUEUE_MAIN, taskId)
              .lpush(`${REDIS_QUEUE_MAIN}:details:${taskId}`, details || '{}')
              .exec();

            const updatedTask = deserializeTask(await redis.hgetall(taskKey));
            io.emit('taskUpdate', updatedTask);
          } else {
            console.log(`Task ${taskId} exceeded max retries, marking as failed`);

            await redis.multi()
              .hset(taskKey, 'status', 'failed')
              .hset(taskKey, 'finishedAt', new Date().toISOString())
              .lrem(REDIS_QUEUE_PROCESSING, 0, taskId)
              .exec();

            const updatedTask = deserializeTask(await redis.hgetall(taskKey));
            io.emit('taskUpdate', updatedTask);
          }
        }
      }
    }
  } catch (error) {
    console.error('Periodic check error:', error);
  }
});

redisSub.subscribe('task:completed', 'task:failed', 'task:progress', (err) => {
  if (err) {
    console.error('Redis subscription error:', err);
  }
});

redisSub.on('message', (channel, message) => {
  try {
    const data = JSON.parse(message);
    io.emit(channel, data);
  } catch (error) {
    console.error('Error handling Redis message:', error);
  }
});

io.on('connection', (socket) => {
  console.log('Client connected');

  redis.keys('task:*').then(taskKeys => {
    const validTaskKeys = taskKeys.filter(k => !k.includes(':details:') && !k.includes(':retry'));
    return Promise.all(validTaskKeys.map(key =>
      redis.hgetall(key).then(data => data && Object.keys(data).length > 0 ? deserializeTask(data) : null)
    );
  }).then(tasks => {
    const validTasks = tasks.filter(Boolean).sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt));
    socket.emit('initialData', { pipelines, tasks: validTasks.slice(0, 100) });
  }).catch(error => {
    console.error('Error sending initial data:', error);
    socket.emit('initialData', { pipelines, tasks: [] });
  });
});

server.listen(PORT, () => {
  console.log(`Node.js 服务运行在 http://localhost:${PORT}`);
  console.log(`Redis: ${REDIS_HOST}:${REDIS_PORT}`);
});
