import { createFileRoute, Link } from "@tanstack/react-router";
import { CodeBlock } from "../../components/CodeBlock";
import { FullRef } from "../../components/FullRef";
import { Kbd } from "../../components/Kbd";
import { Pager } from "../../components/Pager";
import { pageHead } from "../../lib/head";

export const Route = createFileRoute("/docs/ask-ai")({
  head: () =>
    pageHead({
      title: "Ask AI · Eddy docs",
      description:
        "Turn on Ask AI with the Anthropic API or AWS Bedrock: API key, an AWS IAM role through IRSA or Pod Identity, IAM policy, Guardrails, model choice, limits and kill switches.",
      path: "/docs/ask-ai/",
    }),
  component: Page,
});

function Page() {
  return (
    <>
      <h1>Ask AI</h1>
      <p className="lead">
        Ask AI answers questions about a resource using only what the asking user can see. It is read-only and
        off by default. You pick the provider: the Anthropic API, or AWS Bedrock, where data stays in your AWS
        account and region.
      </p>
      <FullRef path="docs/install.md#7-optional-ask-ai" label="docs/install.md, Ask AI" />

      <h2 id="how">How it works</h2>
      <ul>
        <li>
          The only tools are <code>get_resource</code>, <code>get_events</code>, <code>search_resources</code>{" "}
          and, if you enable it, <code>get_logs</code>. No write tool exists.
        </li>
        <li>
          Every tool call runs as the asking user, through the same RBAC filtering as the UI. The output is
          redacted (tokens, keys, PEM blocks, URLs with passwords) before it reaches the model.
        </li>
        <li>
          Cluster data is wrapped as untrusted content with a per-request random delimiter. Answers render
          without images or raw HTML, and suggested commands are copy-only.
        </li>
        <li>
          Each ask is stored in a private thread, so people can come back to it. Press <Kbd>a</Kbd> on a
          selected resource to ask.
        </li>
      </ul>

      <h2 id="anthropic">Option A: the Anthropic API</h2>
      <p>
        Resource summaries and redacted YAML go to Anthropic. The UI says which provider handles the data.
      </p>
      <ol>
        <li>
          Create an API key in the Anthropic console, and add it to <code>eddy-credentials</code> under the
          key <code>ANTHROPIC_API_KEY</code>, with the{" "}
          <Link to="/docs/install/" hash="add-key">
            add-a-key command
          </Link>
          .
        </li>
        <li>
          Enable it in <code>hub-values.yaml</code>, then <code>helm upgrade</code>:
          <CodeBlock
            code={`
credentialsSecret: eddy-credentials

config:
  ai:
    enabled: true
    provider: anthropic
    anthropic:
      model: claude-haiku-4-5-20251001
`}
          />
        </li>
      </ol>
      <p>
        The key is read from the variable named by <code>anthropic.apiKeyEnv</code> (default{" "}
        <code>ANTHROPIC_API_KEY</code>). The model defaults to <code>claude-haiku-4-5-20251001</code>.
      </p>

      <h2 id="bedrock">Option B: AWS Bedrock</h2>
      <p>
        Recommended on AWS: there are no static keys, and data stays in your account and region. The hub gets
        AWS credentials from an <strong>AWS IAM role</strong>, through IRSA or EKS Pod Identity.
      </p>
      <p>
        That IAM role is unrelated to the Kubernetes <code>eddy-viewer</code> and <code>eddy-operator</code>{" "}
        roles. Ask AI needs no extra Kubernetes RBAC, because every tool call runs as the asking user.
      </p>
      <ol>
        <li>
          <strong>Check model access.</strong> Bedrock models are available by default. For Anthropic models,
          AWS asks once per account or organization for a use-case form (First Time Use): open the model in
          the Bedrock console's model catalog. The first call to a third-party model also subscribes the
          account through AWS Marketplace, which needs <code>aws-marketplace:Subscribe</code>,{" "}
          <code>Unsubscribe</code> and <code>ViewSubscriptions</code>. Make that first call yourself, for
          example in the playground, so the hub's role needs no Marketplace permissions.
        </li>
        <li>
          <strong>Create an AWS IAM role for the hub pod</strong> that the hub's ServiceAccount (
          <code>eddy-hub</code> in the <code>eddy</code> namespace) can assume. For IRSA, the trust policy is:
          <CodeBlock
            code={`
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "arn:aws:iam::111122223333:oidc-provider/oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLED539D4633E53DE1B71EXAMPLE"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {"StringEquals": {
      "oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLED539D4633E53DE1B71EXAMPLE:sub": "system:serviceaccount:eddy:eddy-hub",
      "oidc.eks.eu-central-1.amazonaws.com/id/EXAMPLED539D4633E53DE1B71EXAMPLE:aud": "sts.amazonaws.com"
    }}
  }]
}
`}
          />
          For EKS Pod Identity (with the Pod Identity Agent add-on), trust the Pod Identity service instead,
          then associate the role with the ServiceAccount:
          <CodeBlock
            code={`
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Service": "pods.eks.amazonaws.com"},
    "Action": ["sts:AssumeRole", "sts:TagSession"]
  }]
}
`}
          />
          <CodeBlock
            code={`
$ aws eks create-pod-identity-association --cluster-name <management-cluster> \\
    --namespace eddy --service-account eddy-hub \\
    --role-arn arn:aws:iam::111122223333:role/eddy-hub-bedrock
`}
          />
        </li>
        <li>
          <strong>Attach a permissions policy</strong> that allows <code>bedrock:InvokeModel</code> (it also
          covers the Converse API the hub uses) on only the inference profile and, through it, the foundation
          model. The region wildcard covers every region the profile routes to, and the condition keeps it
          usable only through that profile. An <code>eu.</code> profile stays inside Europe, while a{" "}
          <code>global.</code> profile can route anywhere, so pick a geo or single-region profile if data
          residency matters. Drop the last statement if you set no guardrail.
          <CodeBlock
            code={`
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "InvokeViaInferenceProfile",
      "Effect": "Allow",
      "Action": "bedrock:InvokeModel",
      "Resource": "arn:aws:bedrock:eu-central-1:111122223333:inference-profile/eu.anthropic.claude-haiku-4-5-20251001-v1:0"
    },
    {
      "Sid": "InvokeUnderlyingModelOnlyViaThatProfile",
      "Effect": "Allow",
      "Action": "bedrock:InvokeModel",
      "Resource": "arn:aws:bedrock:*::foundation-model/anthropic.claude-haiku-4-5-20251001-v1:0",
      "Condition": {"StringLike": {"bedrock:InferenceProfileArn": "arn:aws:bedrock:eu-central-1:111122223333:inference-profile/eu.anthropic.claude-haiku-4-5-20251001-v1:0"}}
    },
    {
      "Sid": "ApplyGuardrail",
      "Effect": "Allow",
      "Action": "bedrock:ApplyGuardrail",
      "Resource": "arn:aws:bedrock:eu-central-1:111122223333:guardrail/abc123xyz"
    }
  ]
}
`}
          />
          To list the regions explicitly instead, put the ARNs from this command in <code>Resource</code>. The
          list can change over time:
          <CodeBlock
            code={`
$ aws bedrock get-inference-profile --region eu-central-1 \\
    --inference-profile-identifier eu.anthropic.claude-haiku-4-5-20251001-v1:0 \\
    --query 'models[].modelArn'
`}
          />
        </li>
        <li>
          <strong>Annotate the ServiceAccount and enable Bedrock</strong> in the hub values. With Pod
          Identity, leave out the annotation (the association from step 2 does that job):
          <CodeBlock
            code={`
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::111122223333:role/eddy-hub-bedrock

config:
  ai:
    enabled: true
    provider: bedrock
    bedrock:
      region: eu-central-1
      modelId: eu.anthropic.claude-haiku-4-5-20251001-v1:0   # inference profile id or ARN
      guardrail: {id: "abc123xyz", version: "1", trace: false}   # optional
`}
          />
        </li>
      </ol>
      <p>
        <code>region</code> and <code>modelId</code> are required with Bedrock. If Bedrock denies a call,
        compare the ARNs in the error with your policy, because the regions behind a profile can change.
      </p>
      <h3 id="models">Which model</h3>
      <p>
        Claude is the default and the tested choice. The hub talks to Bedrock through the Converse API with
        tool use, so other Converse models that support tool use, such as Amazon Nova, should work. They are
        not tested, so try one on a few real questions before you rely on it, and set <code>modelId</code> to
        its inference profile.
      </p>

      <h2 id="guard">Guardrails and limits</h2>
      <ul>
        <li>
          <strong>Bedrock Guardrails.</strong> Set <code>guardrail.id</code> and <code>version</code> to apply
          one to each call. <code>trace: true</code> adds the guardrail trace to the response for debugging.
          The role needs <code>bedrock:ApplyGuardrail</code> on it (the last statement above).
        </li>
        <li>
          <strong>Logs are off.</strong> <code>config.ai.allowLogs</code> defaults to <code>false</code>. Turn
          it on only if you want <code>get_logs</code> and log attachments to reach the model.
        </li>
        <li>
          <strong>Per-ask limits.</strong> Defaults under <code>config.ai.limits</code>:{" "}
          <code>maxRounds: 6</code> tool rounds, <code>maxToolResultBytes: 24576</code>,{" "}
          <code>maxOutputTokens: 1024</code>, <code>timeout: 60s</code> and <code>perUserPerHour: 30</code>.
          Over the hourly limit a user gets a rate-limit error.
        </li>
        <li>
          <strong>Retention.</strong> Ask threads are kept 30 days by default (
          <code>store.retention.askThreadsDays</code>).
        </li>
      </ul>

      <h2 id="kill">Kill switch</h2>
      <p>
        To turn Ask AI off without a restart, set <code>runtimeFlags.aiEnabled: false</code> in the hub
        values, or patch the <code>flags.yaml</code> key of the <code>eddy-runtime</code> ConfigMap for an
        immediate change. Ask AI then returns 503 and its button disappears. A flag can only turn a feature
        off, it cannot enable what <code>config.ai.enabled</code> disables. See{" "}
        <Link to="/docs/operations/" hash="kill">
          Operations
        </Link>
        .
      </p>

      <h2 id="verify">Verify</h2>
      <ol>
        <li>
          Check the install notes. With AI enabled they say <q>Ask AI is enabled with provider &hellip;</q>:
          <CodeBlock lines={["$ helm -n eddy get notes eddy-hub"]} />
        </li>
        <li>
          In the UI, select a resource and press <Kbd>a</Kbd>. Ask <q>why is this not ready?</q> You should
          get an answer with the tool steps it ran.
        </li>
        <li>
          The audit log shows the asks with <code>via: askai</code>.
        </li>
      </ol>
      <h2 id="trouble">Troubleshooting</h2>
      <ul>
        <li>
          <strong>The hub fails to start after enabling.</strong> With Bedrock both{" "}
          <code>bedrock.region</code> and <code>bedrock.modelId</code> are required. With Anthropic, check the
          Secret has <code>ANTHROPIC_API_KEY</code>.
        </li>
        <li>
          <strong>Bedrock access denied.</strong> The Anthropic use-case form is missing, the first call could
          not subscribe the account in AWS Marketplace, the role is not attached to the ServiceAccount, or the
          policy ARNs do not match the profile. Compare the ARNs in the error with the policy.
        </li>
        <li>
          <strong>No Ask AI button.</strong> It is off in <code>config.ai.enabled</code> or switched off in{" "}
          <code>eddy-runtime</code>.
        </li>
        <li>
          <strong>Egress blocked.</strong> With <code>networkPolicy</code> on, the default egress allows HTTPS
          to anywhere. Tighten it only if you allow the provider's endpoints. Pod Identity also needs port 80
          to the node-local agent:
          <CodeBlock
            code={`
networkPolicy:
  egress:
    extra:
      - to: [{ipBlock: {cidr: 169.254.170.23/32}}]
        ports: [{port: 80, protocol: TCP}]
`}
          />
        </li>
      </ul>
      <Pager current="/docs/ask-ai/" />
    </>
  );
}
