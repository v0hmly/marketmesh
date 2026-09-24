import type {
  Reporter,
  TestCase,
  TestResult,
  FullResult,
} from "@playwright/test/reporter";
/** Real tests must never print passwords, opaque cookies or invite URLs on failure. */
export default class SafeReporter implements Reporter {
  onTestEnd(test: TestCase, result: TestResult) {
    for (const error of result.errors) {
      const places = error.stack?.match(/sso\.spec\.ts:\d+:\d+/g) ?? [];
      for (const place of new Set(places))
        process.stdout.write(`Assertion location: ${place}\n`);
    }
    process.stdout.write(`${result.status}: ${test.title}\n`);
  }
  onError() {
    process.stdout.write("Staff browser runner failed before completion.\n");
  }
  onEnd(result: FullResult) {
    process.stdout.write(`Staff browser suite: ${result.status}\n`);
  }
}
