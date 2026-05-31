#!/usr/bin/env bash
# Update qcom-ec2-role IAM policy to allow DynamoDB access in the target region.
# Run once after migrating QComTable to a new AWS region.
#
# Usage: AWS_DEFAULT_REGION=ap-southeast-2 ./scripts/update-dynamodb-iam-policy.sh [region]
# Default region: ap-southeast-2

set -euo pipefail

REGION="${1:-ap-southeast-2}"
ACCOUNT_ID="$(aws sts get-caller-identity --query Account --output text)"
TABLE="${DYNAMODB_TABLE_NAME:-QComTable}"
ROLE="qcom-ec2-role"
POLICY="qcom-dynamodb-s3"

DOC=$(cat <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "dynamodb:GetItem",
        "dynamodb:PutItem",
        "dynamodb:UpdateItem",
        "dynamodb:DeleteItem",
        "dynamodb:Query",
        "dynamodb:Scan",
        "dynamodb:BatchWriteItem",
        "dynamodb:BatchGetItem",
        "dynamodb:DescribeTable"
      ],
      "Resource": [
        "arn:aws:dynamodb:${REGION}:${ACCOUNT_ID}:table/${TABLE}",
        "arn:aws:dynamodb:${REGION}:${ACCOUNT_ID}:table/${TABLE}/*"
      ]
    },
    {
      "Effect": "Allow",
      "Action": [
        "s3:GetObject",
        "s3:PutObject",
        "s3:DeleteObject",
        "s3:ListBucket",
        "s3:GetBucketLocation"
      ],
      "Resource": [
        "arn:aws:s3:::printdrop-documents",
        "arn:aws:s3:::printdrop-documents/*"
      ]
    }
  ]
}
EOF
)

echo "Updating ${ROLE}/${POLICY} for DynamoDB in ${REGION}..."
aws iam put-role-policy --role-name "$ROLE" --policy-name "$POLICY" --policy-document "$DOC"
echo "Done."
