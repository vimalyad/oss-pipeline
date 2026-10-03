Fixtures copied verbatim from the upstream repositories they are named after,
at the commit the clone in `work/` was on. They are inputs to the resolver
tests, not code this project builds.

They are real rather than hand-written on purpose. A workflow file invented to
exercise the parser agrees with the parser by construction; these ones disagree
with it, which is how the matrix-expression and path-filter defects were found.

  cli__cli/                 MIT
  huggingface__datasets/    Apache-2.0
  kornia__kornia/           Apache-2.0
  pytorch__vision/          BSD-3-Clause

pytorch/vision keeps all twelve workflows. The assertion is that none of them
yields a recipe, so dropping any of them would weaken it.
