import type {
  Reporter,
  TestCase,
  TestResult,
  FullResult,
  TestError,
} from '@playwright/test/reporter';

// Playwright call logs can include filled passwords or cookie assertion values.
// Real-stack runs deliberately report only static test names and source locations.
export default class SafeReporter implements Reporter {
  onTestEnd(test: TestCase, result: TestResult) {
    for (const error of result.errors) {
      const location = error.stack?.match(/account\.spec\.ts:\d+:\d+/)?.[0];
      if (location) process.stdout.write(`Assertion location: ${location}\n`);
    }
    process.stdout.write(
      `${result.status}: ${test.title} (${test.location.file.split('/').pop()}:${test.location.line})\n`,
    );
  }
  onError(error: TestError) {
    // Never print arbitrary error messages: a runner error can include request data.
    const code =
      /\b(EACCES|EPERM|ENOENT|ENOSPC|EBUSY|ERR_MODULE_NOT_FOUND)\b/.exec(
        error.message ?? '',
      )?.[1] ?? 'unclassified';
    process.stdout.write(`Real browser runner failed before test completion (${code}).\n`);
  }
  onEnd(result: FullResult) {
    process.stdout.write(`Real browser suite: ${result.status}\n`);
  }
}
