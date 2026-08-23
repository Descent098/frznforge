/**
 * Minimal `node:readline/promises` wrapper for `npm run frznforge -- init`.
 *
 * Kept deliberately dumb: no spinners, no raw mode, no ANSI cursor tricks. The init flow is
 * a handful of questions, and a plain line-based prompt behaves the same in Windows
 * Terminal, PowerShell, Git Bash and a piped shell.
 *
 * Answers are read from our own `line` queue rather than `rl.question()`: when stdin is a
 * pipe every buffered line arrives in one chunk, and `question()` drops the ones that land
 * while no question is pending. Queueing makes `printf '1\nme\n' | …` work the same as
 * typing, which is what makes the flow testable by hand.
 *
 * Nothing here ever reads a secret — tokens come from the environment only, so there is
 * deliberately no masked-input helper.
 */
import { createInterface, type Interface } from 'node:readline/promises';
import type { Readable, Writable } from 'node:stream';

export interface Choice<T> {
  value: T;
  label: string;
  /** Shown after the label, dash-separated. */
  hint?: string;
}

/** Thrown when stdin ends while a question is waiting — the alternative is an endless loop. */
export class InputClosedError extends Error {
  constructor() {
    super('input ended before the question was answered');
    this.name = 'InputClosedError';
  }
}

export class Prompter {
  private readonly rl: Interface;
  private readonly output: Writable;
  private readonly buffered: string[] = [];
  private readonly waiting: Array<(line: string | null) => void> = [];
  private closed = false;

  constructor(input: Readable = process.stdin, output: Writable = process.stdout) {
    this.output = output;
    this.rl = createInterface({ input, output, terminal: false });
    this.rl.on('line', (line) => {
      const waiter = this.waiting.shift();
      if (waiter) waiter(line);
      else this.buffered.push(line);
    });
    this.rl.on('close', () => {
      this.closed = true;
      for (const waiter of this.waiting.splice(0)) waiter(null);
    });
  }

  /** Write a line to the prompt's output stream (kept off `console` so tests can capture it). */
  write(line = ''): void {
    this.output.write(`${line}\n`);
  }

  private nextLine(): Promise<string | null> {
    const buffered = this.buffered.shift();
    if (buffered !== undefined) return Promise.resolve(buffered);
    if (this.closed) return Promise.resolve(null);
    return new Promise((resolve) => this.waiting.push(resolve));
  }

  /** Ask a free-text question. Empty input returns `fallback`. */
  async ask(question: string, fallback = ''): Promise<string> {
    this.output.write(`${question}${fallback ? ` [${fallback}]` : ''}: `);
    const line = await this.nextLine();
    if (line === null) throw new InputClosedError();
    const answer = line.trim();
    return answer === '' ? fallback : answer;
  }

  /** Ask a free-text question until the answer is non-empty. */
  async askRequired(question: string, fallback = ''): Promise<string> {
    for (;;) {
      const answer = await this.ask(question, fallback);
      if (answer !== '') return answer;
      this.write('  a value is required.');
    }
  }

  /** Numbered single choice. Re-asks until the answer is in range. */
  async choose<T>(question: string, choices: Choice<T>[], defaultIndex = 0): Promise<T> {
    choices.forEach((c, i) => this.write(`  ${i + 1}. ${c.label}${c.hint ? ` — ${c.hint}` : ''}`));
    for (;;) {
      const raw = await this.ask(question, String(defaultIndex + 1));
      const n = Number.parseInt(raw, 10);
      if (Number.isInteger(n) && n >= 1 && n <= choices.length) return choices[n - 1]!.value;
      this.write(`  pick a number between 1 and ${choices.length}.`);
    }
  }

  /** y/N (or Y/n when `defaultYes`). Anything unrecognised re-asks rather than guessing. */
  async confirm(question: string, defaultYes = false): Promise<boolean> {
    for (;;) {
      const raw = (await this.ask(`${question} ${defaultYes ? '[Y/n]' : '[y/N]'}`)).toLowerCase();
      if (raw === '') return defaultYes;
      if (raw === 'y' || raw === 'yes') return true;
      if (raw === 'n' || raw === 'no') return false;
      this.write('  answer y or n.');
    }
  }

  close(): void {
    this.rl.close();
  }
}
