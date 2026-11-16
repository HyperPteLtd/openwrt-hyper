/** HTTP echo fixture for http_test.uc. Usage: node echo_server.mjs <port> */
import { createServer } from 'node:http';
const server = createServer(async (req, res) => {
  if (req.url === '/redirect') {
    res.writeHead(307, { Location: '/redirect-target' });
    res.end();
    return;
  }
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  const bytes = Buffer.concat(chunks);
  const body = JSON.stringify({ method: req.method, path: req.url, headers: req.headers, body: bytes.length ? bytes.toString('utf8') : null });
  res.writeHead(req.url.startsWith('/status/') ? Number(req.url.split('/')[2]) : 200,
    { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) });
  res.end(body);
});
server.listen(Number(process.argv[2]), '127.0.0.1');
