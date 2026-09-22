import net from 'node:net';

export interface CapturedMail {
  from: string;
  to: string;
  raw: string; // decoded (quoted-printable resolved) message
}

export interface SmtpSink {
  mails: CapturedMail[];
  waitForCount(n: number, timeoutMs?: number): Promise<CapturedMail[]>;
  close(): Promise<void>;
}

function decodeQuotedPrintable(s: string): string {
  return s.replace(/=\r?\n/g, '').replace(/=([0-9A-F]{2})/g, (_, h) => String.fromCharCode(parseInt(h, 16)));
}

// Minimal plaintext SMTP server (no auth/TLS): enough for the API's SMTP sender in the e2e stack.
export function startSmtpSink(port: number): Promise<SmtpSink> {
  const mails: CapturedMail[] = [];
  const server = net.createServer((socket) => {
    let from = '';
    let to = '';
    let inData = false;
    let data = '';
    let buf = '';
    const send = (line: string) => socket.write(line + '\r\n');
    send('220 e2e-sink ESMTP');
    socket.on('data', (chunk) => {
      buf += chunk.toString('utf8');
      let idx: number;
      while ((idx = buf.indexOf('\r\n')) >= 0) {
        const line = buf.slice(0, idx);
        buf = buf.slice(idx + 2);
        if (inData) {
          if (line === '.') {
            inData = false;
            mails.push({ from, to, raw: decodeQuotedPrintable(data) });
            data = '';
            send('250 queued');
          } else {
            data += (line.startsWith('..') ? line.slice(1) : line) + '\r\n';
          }
          continue;
        }
        const cmd = line.toUpperCase();
        if (cmd.startsWith('EHLO') || cmd.startsWith('HELO')) send('250 e2e-sink');
        else if (cmd.startsWith('MAIL FROM:')) { from = line.slice(10).replace(/[<>\s]/g, ''); send('250 ok'); }
        else if (cmd.startsWith('RCPT TO:')) { to = line.slice(8).replace(/[<>\s]/g, ''); send('250 ok'); }
        else if (cmd === 'DATA') { inData = true; send('354 go'); }
        else if (cmd === 'QUIT') { send('221 bye'); socket.end(); }
        else send('250 ok');
      }
    });
    socket.on('error', () => undefined);
  });
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', () =>
      resolve({
        mails,
        async waitForCount(n, timeoutMs = 10_000) {
          const start = Date.now();
          while (mails.length < n) {
            if (Date.now() - start > timeoutMs) throw new Error(`expected ${n} e-mails, got ${mails.length}`);
            await new Promise((r) => setTimeout(r, 100));
          }
          return mails;
        },
        close: () => new Promise<void>((r) => server.close(() => r())),
      }),
    );
  });
}
