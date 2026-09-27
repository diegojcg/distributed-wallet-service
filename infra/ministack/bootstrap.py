"""Provision local queues/identities and prove IAM allows AND denies operations."""
import json
import os
from pathlib import Path
import boto3
from botocore.exceptions import ClientError

endpoint = os.environ['AWS_ENDPOINT_URL']
root = dict(endpoint_url=endpoint, region_name='us-east-1')
iam = boto3.client('iam', **root)
sqs = boto3.client('sqs', **root)
account = boto3.client('sts', **root).get_caller_identity()['Account']
prefix = f'arn:aws:sqs:us-east-1:{account}:'

def queue(name, attributes):
    return sqs.create_queue(QueueName=name, Attributes=attributes)['QueueUrl']

dlq = queue('wager-transactions-dlq.fifo', {'FifoQueue':'true', 'MessageRetentionPeriod':'1209600'})
dlq_arn = sqs.get_queue_attributes(QueueUrl=dlq, AttributeNames=['QueueArn'])['Attributes']['QueueArn']
incoming = queue('wager-transactions.fifo', {
    'FifoQueue':'true', 'ContentBasedDeduplication':'false', 'VisibilityTimeout':'30',
    'ReceiveMessageWaitTimeSeconds':'10', 'MessageRetentionPeriod':'345600',
    'RedrivePolicy':json.dumps({'deadLetterTargetArn':dlq_arn,'maxReceiveCount':5})})
second_dlq = queue('provider-b-wager-transactions-dlq.fifo', {'FifoQueue':'true', 'MessageRetentionPeriod':'1209600'})
second_dlq_arn = sqs.get_queue_attributes(QueueUrl=second_dlq, AttributeNames=['QueueArn'])['Attributes']['QueueArn']
second_input = queue('provider-b-wager-transactions.fifo', {'FifoQueue':'true','ContentBasedDeduplication':'false','VisibilityTimeout':'30','ReceiveMessageWaitTimeSeconds':'10','RedrivePolicy':json.dumps({'deadLetterTargetArn':second_dlq_arn,'maxReceiveCount':5})})
events = queue('wallet-events.fifo', {'FifoQueue':'true','ContentBasedDeduplication':'false',
    'VisibilityTimeout':'30','MessageRetentionPeriod':'345600'})

def identity(name, statements):
    try: user = iam.create_user(UserName=name)['User']
    except iam.exceptions.EntityAlreadyExistsException: user = iam.get_user(UserName=name)['User']
    iam.put_user_policy(UserName=name, PolicyName='least-privilege',
                        PolicyDocument=json.dumps({'Version':'2012-10-17','Statement':statements}))
    path = Path('/credentials') / (name + '.json')
    saved = json.loads(path.read_text()) if path.exists() else None
    keys = iam.list_access_keys(UserName=name)['AccessKeyMetadata']
    if not saved or saved['accessKeyId'] not in [k['AccessKeyId'] for k in keys]:
        for k in keys: iam.delete_access_key(UserName=name, AccessKeyId=k['AccessKeyId'])
        key = iam.create_access_key(UserName=name)['AccessKey']
        saved = {'accessKeyId':key['AccessKeyId'],'secretAccessKey':key['SecretAccessKey']}
    saved['userId'] = user['UserId']
    path.write_text(json.dumps(saved))
    path.chmod(0o600)
    os.chown(path, 0, 0)
    return saved

def allow(actions, name):
    return {'Effect':'Allow','Action':actions,'Resource':prefix+name}

providers={}
queue_names={'provider-a':'wager-transactions.fifo','provider-b':'provider-b-wager-transactions.fifo'}
for name in ['provider-a','provider-b']:
    providers[name] = identity(name, [allow(['sqs:SendMessage','sqs:GetQueueUrl','sqs:GetQueueAttributes'], queue_names[name])])
worker = identity('worker', [
    allow(['sqs:GetQueueUrl','sqs:GetQueueAttributes'], 'wager-transactions-dlq.fifo'),
    allow(['sqs:GetQueueUrl','sqs:GetQueueAttributes'], 'provider-b-wager-transactions-dlq.fifo'),
    allow(['sqs:GetQueueUrl','sqs:GetQueueAttributes','sqs:ReceiveMessage','sqs:DeleteMessage','sqs:ChangeMessageVisibility'], 'provider-b-wager-transactions.fifo'),
    allow(['sqs:GetQueueUrl','sqs:GetQueueAttributes','sqs:ReceiveMessage','sqs:DeleteMessage','sqs:ChangeMessageVisibility'], 'wager-transactions.fifo'),
    allow(['sqs:GetQueueUrl','sqs:GetQueueAttributes','sqs:SendMessage'], 'wallet-events.fifo')])
observer = identity('observer', [allow(['sqs:GetQueueUrl','sqs:GetQueueAttributes','sqs:ReceiveMessage','sqs:DeleteMessage'], 'wallet-events.fifo')])
worker['queueProviders'] = {v:k for k,v in queue_names.items()}
worker_path = Path('/credentials/worker.json')
worker_path.write_text(json.dumps(worker))
os.chown(worker_path, 10001, 10001)
worker_path.chmod(0o400)

def client(creds):
    return boto3.client('sqs', **root, aws_access_key_id=creds['accessKeyId'], aws_secret_access_key=creds['secretAccessKey'])

def denied(fn):
    try: fn()
    except ClientError as e:
        if 'AccessDenied' not in e.response['Error']['Code'] and e.response['Error']['Code'] != 'InvalidClientTokenId': raise
        return
    raise RuntimeError('IAM enforcement failed: forbidden operation succeeded')

client(worker).get_queue_attributes(QueueUrl=incoming, AttributeNames=['QueueArn'])
client(providers['provider-a']).get_queue_attributes(QueueUrl=incoming, AttributeNames=['QueueArn'])
denied(lambda: client(providers['provider-a']).receive_message(QueueUrl=incoming))
denied(lambda: client(worker).send_message(QueueUrl=incoming, MessageBody='{}', MessageGroupId='probe', MessageDeduplicationId='forbidden-probe'))
denied(lambda: client(providers['provider-b']).get_queue_attributes(QueueUrl=events, AttributeNames=['QueueArn']))
denied(lambda: client(providers['provider-a']).send_message(QueueUrl=second_input, MessageBody='{}', MessageGroupId='probe', MessageDeduplicationId='forbidden-cross-provider'))
print('Queues provisioned; IAM positive and negative checks passed. No credentials printed.')
