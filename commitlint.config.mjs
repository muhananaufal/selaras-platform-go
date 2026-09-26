// Commit message rules (Conventional Commits), enforced by the commitlint
// workflow on every pull request and push.
//
// JavaScript, not JSON, because of legacyExemptions below: commitlint only
// accepts functions for `ignores`.

// Two commits on develop whose bodies break footer-leading-blank: a wrapped
// body line starting with "word:" is read as a footer. They were merged in
// PRs #11 and #12 while the merge guard let a red commitlint through (fixed
// since, see docs/runbook). History that others have pulled is not
// rewritten, so each one is exempted here - by its exact header AND the exact
// offending line, so a new commit with the same mistake still fails.
const legacyExemptions = [
  {
    sha: "bdf09a4",
    header: "docs(slo): record the live error budget drill",
    line: "stopped: 150 requests failed with 503/504, the availability budget went",
  },
  {
    sha: "2afb92f",
    header: "feat(supply-chain): attest SBOM and SLSA provenance, verify before deploy",
    line: "cluster: signed by this repository's cd.yml, on a GitHub-hosted runner,",
  },
];

const isLegacyExemption = (message) => {
  const lines = message.split("\n");
  return legacyExemptions.some((e) => lines[0] === e.header && lines.includes(e.line));
};

export default {
  extends: ["@commitlint/config-conventional"],
  ignores: [isLegacyExemption],
  rules: {
    "type-enum": [
      2,
      "always",
      [
        "feat",
        "fix",
        "docs",
        "style",
        "refactor",
        "perf",
        "test",
        "build",
        "ci",
        "chore",
        "revert"
      ]
    ],
    "type-case": [
      2,
      "always",
      "lower-case"
    ],
    "type-empty": [
      2,
      "never"
    ],
    "scope-case": [
      0
    ],
    "subject-case": [
      0
    ],
    "subject-empty": [
      2,
      "never"
    ],
    "subject-max-length": [
      0
    ],
    "subject-full-stop": [
      2,
      "never",
      "."
    ],
    "header-max-length": [
      2,
      "always",
      100
    ],
    "body-leading-blank": [
      2,
      "always"
    ],
    "body-max-line-length": [
      0
    ],
    "footer-leading-blank": [
      2,
      "always"
    ],
    "footer-max-line-length": [
      0
    ]
  },
};
