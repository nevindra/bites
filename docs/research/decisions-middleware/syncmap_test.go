package probe

import "sync"

type syncMap struct{ sync.Map }
