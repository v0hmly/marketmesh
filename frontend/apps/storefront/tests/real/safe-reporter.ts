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
    const locations = new Set<string>();
    for (const error of result.errors) {
      for (const location of error.stack?.match(
        /(?:account|security|mail|avatar|registration)\.(?:spec\.)?ts:\d+:\d+/g,
      ) ?? []) {
        if (locations.size < 5) locations.add(location);
      }
    }
    for (const location of locations) process.stdout.write(`Assertion location: ${location}\n`);
    if (result.errors.length) {
      const safe = test.annotations.filter(
        (item) =>
          (item.type === 'account-step' &&
            /^(profile|book-initial|book-relogin|book-foreign|maximum|theme-initial|theme-relogin|theme-foreign):(register|login|ready|logout):(start|done)$/.test(
              item.description ?? '',
            )) ||
          (item.type === 'account-rpc' &&
            /^(RegisterCredentials|StartLogin|CompleteLogin|Login|RefreshSession|Logout|LogoutAll|GetMe|UpdateMe|ListAddresses|CreateAddress|UpdateAddress|DeleteAddress|SetDefaultAddress|GetSettings|UpdateSettings):[1-5][0-9]{2}$/.test(
              item.description ?? '',
            )),
      );
      for (const item of safe.slice(-30))
        process.stdout.write(`Account diagnostic: ${item.description}\n`);
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
