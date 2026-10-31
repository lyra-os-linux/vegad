package dbusserver

// Explicit routing inventory: adding an exported method requires classification.
type methodPrivilege uint8

const (
	queryPublic methodPrivilege = iota
	queryJournal
	// brokeredPublic is a public read, like queryPublic, whose backend only
	// answers root; it runs in the broker without any polkit check.
	brokeredPublic
	privilegedRead
	administrative
	maintenance
	disabledOperation
)

type methodPolicy struct {
	privilege methodPrivilege
	action    string
}

var methodPolicies = map[string]methodPolicy{
	"Preparation.GetPendingKeys":  {queryPublic, ""},
	"Preparation.ApproveKey":      {administrative, ""},
	"Preparation.GetStatus":       {queryPublic, ""},
	"Preparation.Retry":           {administrative, ""},
	"Backup.CreateConfig":         {administrative, ""},
	"Backup.ListConfigs":          {privilegedRead, "org.lyraos.vega.backup.read-admin"},
	"Backup.RunBackupNow":         {administrative, ""},
	"Backup.ListSnapshots":        {privilegedRead, "org.lyraos.vega.backup.read-admin"},
	"Backup.RestoreSnapshot":      {administrative, ""},
	"Backup.DeleteConfig":         {administrative, ""},
	"Backup.ListSnapshotPaths":    {privilegedRead, "org.lyraos.vega.backup.read-admin"},
	"Backup.RestoreItems":         {administrative, ""},
	"Bluetooth.Status":            {queryPublic, ""},
	"Bluetooth.ListDevices":       {queryPublic, ""},
	"Bluetooth.SetPowered":        {administrative, ""},
	"Bluetooth.SetDiscoverable":   {administrative, ""},
	"Bluetooth.SetPairable":       {administrative, ""},
	"Bluetooth.SetScanning":       {administrative, ""},
	"Bluetooth.Pair":              {administrative, ""},
	"Bluetooth.Trust":             {administrative, ""},
	"Bluetooth.Connect":           {administrative, ""},
	"Bluetooth.Disconnect":        {administrative, ""},
	"Bluetooth.Remove":            {administrative, ""},
	"Bluetooth.SendFile":          {administrative, ""},
	"Bluetooth.StartFileReceiver": {administrative, ""},
	// Scan only measures; Clean changes the system (storage.cleanup inside).
	"Cleanup.Scan":    {privilegedRead, "org.lyraos.vega.storage.read-admin"},
	"Cleanup.Clean":   {administrative, ""},
	"DateTime.Status": {queryPublic, ""},
	// Diagnostics keeps the running job in the broker: the reads must see it.
	"Diagnostics.ListTests": {brokeredPublic, ""},
	"Diagnostics.Current":   {brokeredPublic, ""},
	"Diagnostics.Start":     {administrative, ""},
	"Diagnostics.Cancel":    {administrative, ""},
	"Diagnostics.Repair":    {administrative, ""},
	// Ids and indexes only, like Current; the fixes and "seen" are actions.
	"Diagnostics.Remedies":       {brokeredPublic, ""},
	"Diagnostics.RepairFindings": {administrative, ""},
	"Diagnostics.MarkLogsSeen":   {administrative, ""},
	// The weekly check: settings are a plain read, changing them and the
	// last run's texts are checked as usual.
	"Diagnostics.GetPeriodic":  {brokeredPublic, ""},
	"Diagnostics.SetPeriodic":  {administrative, ""},
	"Diagnostics.LastPeriodic": {privilegedRead, "org.lyraos.vega.hardware.read-admin"},
	// Reports hold journal excerpts: the same interactive, retained
	// logs.read-admin as the Logs page, checked inside Report.
	"Diagnostics.Report":     {administrative, ""},
	"DateTime.ListTimezones": {queryPublic, ""},
	"DateTime.ListLocales":   {queryPublic, ""},
	"DateTime.ListKeymaps":   {queryPublic, ""},
	"DateTime.Apply":         {administrative, ""},
	// firewalld authorizes even the active zone through polkit's
	// org.fedoraproject.FirewallD1.info, which the query worker (a system unit
	// outside the caller's session) is denied. Status stays public for vega-web
	// and SSH callers (vegad#79), so it runs in the broker without polkit.
	"Firewall.Status": {brokeredPublic, ""},
	// firewalld's config.info reads require privileged backend access. Keep
	// these two reads in the broker; a user worker would trigger its agent.
	"Firewall.ListServices":             {privilegedRead, "org.lyraos.vega.firewall.read-admin"},
	"Firewall.SetServiceEnabled":        {administrative, ""},
	"Firewall.ListPorts":                {privilegedRead, "org.lyraos.vega.firewall.read-admin"},
	"Firewall.AddPort":                  {administrative, ""},
	"Firewall.RemovePort":               {administrative, ""},
	"Firewall.ListListening":            {privilegedRead, "org.lyraos.vega.firewall.read-admin"},
	"Software.NonFreeFirmwareStatus":    {queryPublic, ""},
	"Software.InstallNonFreeFirmware":   {disabledOperation, ""},
	"Hardware.Inventory":                {queryPublic, ""},
	"Hardware.InventoryLocalized":       {queryPublic, ""},
	"Hardware.SwitchNvidiaDriver":       {disabledOperation, ""},
	"Hardware.FirmwareStatus":           {queryPublic, ""},
	"Hardware.FirmwareStatusLocalized":  {queryPublic, ""},
	"Kernel.ListInstalled":              {queryPublic, ""},
	"Kernel.AvailablePackages":          {queryPublic, ""},
	"Kernel.Install":                    {administrative, ""},
	"Kernel.Remove":                     {administrative, ""},
	"Kernel.BootStatus":                 {privilegedRead, "org.lyraos.vega.kernel.read-admin"},
	"Kernel.ListBootEntries":            {privilegedRead, "org.lyraos.vega.kernel.read-admin"},
	"Kernel.ApplyBootConfig":            {administrative, ""},
	"Logs.Query":                        {queryJournal, "org.lyraos.vega.logs.read-admin"},
	"Logs.ListUnits":                    {queryJournal, "org.lyraos.vega.logs.read-admin"},
	"Metadata.Profile":                  {queryPublic, ""},
	"Metadata.Version":                  {queryPublic, ""},
	"Metadata.Capabilities":             {queryPublic, ""},
	"Monitor.Metrics":                   {queryPublic, ""},
	"Monitor.ListProcesses":             {queryPublic, ""},
	"Monitor.KillProcess":               {administrative, ""},
	"Network.ListInterfaces":            {queryPublic, ""},
	"Network.ListWifi":                  {queryPublic, ""},
	"Network.ConnectWifi":               {administrative, ""},
	"Network.Disconnect":                {administrative, ""},
	"Network.SetStaticIPv4":             {administrative, ""},
	"Network.ImportVPN":                 {administrative, ""},
	"Network.GetProxy":                  {queryPublic, ""},
	"Network.SetProxy":                  {administrative, ""},
	"Software.NvidiaStatus":             {disabledOperation, ""},
	"Software.NvidiaRecovery":           {disabledOperation, ""},
	"Software.InstallNvidia":            {disabledOperation, ""},
	"Software.CheckNvidia":              {disabledOperation, ""},
	"Services.ListServices":             {queryPublic, ""},
	"Services.ListServicesLocalized":    {queryPublic, ""},
	"Services.ListAllServices":          {queryPublic, ""},
	"Services.ListAllServicesLocalized": {queryPublic, ""},
	"Services.SetServiceEnabled":        {administrative, ""},
	"Services.SetServiceRunning":        {administrative, ""},
	"Services.RestartService":           {administrative, ""},
	"Snapshots.Available":               {queryPublic, ""},
	"Snapshots.ListSnapshots":           {privilegedRead, "org.lyraos.vega.snapshots.read-admin"},
	"Snapshots.CreateSnapshot":          {administrative, ""},
	"Snapshots.DiffPackages":            {privilegedRead, "org.lyraos.vega.snapshots.read-admin"},
	"Snapshots.DiffPackagesLocalized":   {privilegedRead, "org.lyraos.vega.snapshots.read-admin"},
	"Snapshots.Rollback":                {administrative, ""},
	"Snapshots.DeleteSnapshot":          {administrative, ""},
	"Snapshots.ClearSnapshots":          {administrative, ""},
	"Snapshots.SetRetentionPolicy":      {administrative, ""},
	"Software.PackageManagerName":       {queryPublic, ""},
	"Software.GetPackageDetails":        {queryPublic, ""},
	"Software.Search":                   {queryPublic, ""},
	"Software.SearchNative":             {queryPublic, ""},
	"Software.Install":                  {administrative, ""},
	"Software.Remove":                   {administrative, ""},
	"Software.ListUpdates":              {queryPublic, ""},
	"Software.ListNativeUpdates":        {queryPublic, ""},
	"Software.GetUpdateStatus":          {queryPublic, ""},
	"Software.RequestUpdateCheck":       {maintenance, ""},
	"Software.ListInstalled":            {queryPublic, ""},
	"Software.ListNativeInstalled":      {queryPublic, ""},
	"Software.UpdateAll":                {administrative, ""},
	"Software.UpdateAllNative":          {administrative, ""},
	"Software.UpdatePackage":            {administrative, ""},
	"Software.ListRepos":                {queryPublic, ""},
	"Software.SetRepoEnabled":           {administrative, ""},
	"Software.ClearCache":               {administrative, ""},
	"Software.ClearNativeCache":         {administrative, ""},
	"Software.AddRepo":                  {administrative, ""},
	"Software.TrustRepoKey":             {administrative, ""},
	"Storage.ListVolumes":               {queryPublic, ""},
	"Storage.Mount":                     {administrative, ""},
	"Storage.Unmount":                   {administrative, ""},
	"System.Version":                    {queryPublic, ""},
	"System.Ping":                       {queryPublic, ""},
	"System.Distro":                     {queryPublic, ""},
	"System.Logo":                       {queryPublic, ""},
	"System.DiskUsage":                  {queryPublic, ""},
	"Users.ListUsers":                   {queryPublic, ""},
	"Users.ListGroups":                  {queryPublic, ""},
	"Users.CreateUser":                  {administrative, ""},
	"Users.UpdateUser":                  {administrative, ""},
	"Users.RemoveUser":                  {administrative, ""},
	"Users.SetAdmin":                    {administrative, ""},
}
