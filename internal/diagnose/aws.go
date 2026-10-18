package diagnose

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

// awsConfig is the minimal AWS config surface used by the bedrock probe.
type awsConfig interface {
	Creds() aws.CredentialsProvider
}

type awsConfigWrap struct{ cfg *aws.Config }

func (w awsConfigWrap) Creds() aws.CredentialsProvider { return w.cfg.Credentials }

// awsLoadConfig resolves the AWS SDK config for the given profile/region
// (credential chain only: shared config, env vars, IMDS). No network call is
// made here - credentials retrieval is checked separately by the caller.
func awsLoadConfig(profile, region string) (awsConfig, error) {
	var opts []func(*awsconfig.LoadOptions) error
	if profile != "" {
		opts = append(opts, awsconfig.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), opts...)
	if err != nil {
		return nil, err
	}
	return awsConfigWrap{cfg: &cfg}, nil
}
