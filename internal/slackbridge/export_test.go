package slackbridge

func (c *userCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
