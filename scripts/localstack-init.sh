#!/bin/bash
echo "Creating S3 bucket: printdrop-documents"
awslocal s3 mb s3://printdrop-documents --region ap-southeast-2
echo "S3 bucket created successfully"
