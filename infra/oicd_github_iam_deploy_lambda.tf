############################################
# GitHub Actions OIDC -> WebDAV Lambda
############################################

data "aws_iam_policy_document" "github_webdav_deployer_assume_role" {
  statement {
    sid    = "AllowGitHubActionsWebIdentity"
    effect = "Allow"

    principals {
      type = "Federated"

      identifiers = [
        data.aws_iam_openid_connect_provider.github.arn
      ]
    }

    actions = [
      "sts:AssumeRoleWithWebIdentity"
    ]

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"

      values = [
        "sts.amazonaws.com"
      ]
    }

    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"

      values = [
        local.github_sub
      ]
    }
  }
}

resource "aws_iam_role" "github_webdav_deployer" {
  name = "${local.project_full_name}-github-deployer"

  assume_role_policy = data.aws_iam_policy_document.github_webdav_deployer_assume_role.json
}

data "aws_iam_policy_document" "github_webdav_deployer_permissions" {
  statement {
    sid    = "UpdateWebDAVLambdaCode"
    effect = "Allow"

    actions = [
      "lambda:UpdateFunctionCode",
      "lambda:GetFunction",
      "lambda:GetFunctionConfiguration"
    ]

    resources = [
      aws_lambda_function.webdav.arn
    ]
  }
}

resource "aws_iam_role_policy" "github_webdav_deployer" {
  name = "${local.project_full_name}-lambda-deploy"
  role = aws_iam_role.github_webdav_deployer.id

  policy = data.aws_iam_policy_document.github_webdav_deployer_permissions.json
}