package cli

import (
	"github.com/flaggx/vpsdeploymentautomation/internal/db"
	"github.com/spf13/cobra"
)

var (
	resetDBPassword bool
	saveDBSecret    bool
	backupUpload    bool
	restoreFile     string
	restoreYes      bool
	scheduleUpload  bool
	scheduleHour    int
	replicaHost     string
	poolerPort      int
)

func newDBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "db",
		Short: "Manage PostgreSQL on the VPS",
		Long:  "Install and configure PostgreSQL with optional backups, dedicated hosts, replicas, and PgBouncer.",
	}

	cmd.AddCommand(newDBBootstrapCmd())
	cmd.AddCommand(newDBStatusCmd())
	cmd.AddCommand(newDBBackupCmd())
	cmd.AddCommand(newDBBackupsCmd())
	cmd.AddCommand(newDBRestoreCmd())
	cmd.AddCommand(newDBScheduleCmd())
	cmd.AddCommand(newDBReplicaCmd())
	cmd.AddCommand(newDBPoolerCmd())

	return cmd
}

func newDBBootstrapCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Install PostgreSQL and create the environment database",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForConnection()
			if err != nil {
				return err
			}
			_, err = db.Bootstrap(cfg, db.BootstrapOptions{
				ResetPassword: resetDBPassword,
				SaveSecret:    saveDBSecret,
			})
			return err
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	cmd.Flags().BoolVar(&resetDBPassword, "reset-password", false, "Generate a new database password")
	cmd.Flags().BoolVar(&saveDBSecret, "save-secret", false, "Save DATABASE_URL to local secrets automatically")
	return cmd
}

func newDBStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show PostgreSQL status for an environment",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForConnection()
			if err != nil {
				return err
			}
			return db.Status(cfg)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	return cmd
}

func newDBBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Create a PostgreSQL dump on the DB host",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForConnection()
			if err != nil {
				return err
			}
			return db.Backup(cfg, db.BackupOptions{Upload: backupUpload})
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	cmd.Flags().BoolVar(&backupUpload, "upload", false, "Upload dump to configured S3-compatible bucket")
	return cmd
}

func newDBBackupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backups",
		Short: "List local PostgreSQL dumps on the DB host",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForConnection()
			if err != nil {
				return err
			}
			return db.ListBackups(cfg)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	return cmd
}

func newDBRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a dump into the environment database (destructive)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForConnection()
			if err != nil {
				return err
			}
			return db.Restore(cfg, restoreFile, restoreYes)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	cmd.Flags().StringVar(&restoreFile, "file", "", "Absolute path to .dump on the DB host")
	cmd.Flags().BoolVar(&restoreYes, "yes", false, "Confirm destructive restore")
	return cmd
}

func newDBScheduleCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "schedule",
		Short: "Install a daily systemd timer for PostgreSQL backups",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForConnection()
			if err != nil {
				return err
			}
			return db.Schedule(cfg, db.ScheduleOptions{
				Upload: scheduleUpload,
				Hour:   scheduleHour,
			})
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	cmd.Flags().BoolVar(&scheduleUpload, "upload", false, "Also upload each scheduled dump to S3-compatible storage")
	cmd.Flags().IntVar(&scheduleHour, "hour", 3, "UTC hour for daily backup (0-23)")
	return cmd
}

func newDBReplicaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replica",
		Short: "Manage streaming read replicas",
	}
	boot := &cobra.Command{
		Use:   "bootstrap",
		Short: "Bootstrap a streaming standby on --replica-host",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForConnection()
			if err != nil {
				return err
			}
			return db.BootstrapReplica(cfg, db.ReplicaOptions{
				ReplicaHost: replicaHost,
				SaveSecret:  saveDBSecret,
			})
		},
	}
	boot.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	boot.Flags().StringVar(&replicaHost, "replica-host", "", "IP/hostname of the standby VPS")
	boot.Flags().BoolVar(&saveDBSecret, "save-secret", false, "Remind to save DATABASE_READ_URL (password not auto-copied)")
	cmd.AddCommand(boot)
	return cmd
}

func newDBPoolerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pooler",
		Short: "Install PgBouncer connection pooler on the DB host",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigForConnection()
			if err != nil {
				return err
			}
			return db.BootstrapPooler(cfg, db.PoolerOptions{ListenPort: poolerPort})
		},
	}
	cmd.Flags().StringVar(&envName, "env", "prod", "Environment name from vpsdeploy.toml")
	cmd.Flags().IntVar(&poolerPort, "port", 6432, "PgBouncer listen port")
	return cmd
}
