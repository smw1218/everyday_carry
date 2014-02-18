package edc

import (
	"fmt"
	"github.com/fitstar/falcore"
	"github.com/vmihailenco/redis"
	"hash/crc32"
	"sort"
	"time"
)

type Controller struct {
	client *redis.Client
	ww     *WebsocketWorker
}

func NewController(host string, port int, ww *WebsocketWorker) (*Controller, error) {
	client := redis.NewTCPClient(fmt.Sprintf("%v:%v", host, port), "", -1)
	controller := &Controller{
		client: client,
		ww:     ww,
	}
	// add all my routes
	ww.NewConnection = controller.NewConnectionHandler
	ww.AddRoute("answer", controller.HandleAnswer)
	ww.AddRoute("select-question", controller.HandleSelectedQuestion)
	go controller.StatSender()
	return controller, nil
}

func (s *Controller) Close() error {
	return s.client.Close()
}

func (s *Controller) StatSender() {
	timer := time.NewTicker(5 * time.Second)
	lastTime := time.Now()
	lastMc := s.ww.GetMessagesSent()
	lastBc := s.ww.GetBytesSent()
	for now := range timer.C {
		acc := s.ww.GetActiveClientCount()
		if acc > 0 {
			diff := float64(now.Sub(lastTime)) / float64(time.Second)
			mc := s.ww.GetMessagesSent()
			bc := s.ww.GetBytesSent()
			mps := float64(mc-lastMc) / diff
			bps := float64(bc-lastBc) / diff
			// round
			mps = float64(int64(mps*100.0+0.5)) / 100.0
			bps = float64(int64(bps*100.0+0.5)) / 100.0
			lastMc = mc
			lastBc = bc
			lastTime = now

			sp := &ServerPush{
				Method: "stats",
				Data: map[string]interface{}{
					"acc": acc,
					"bps": bps,
					"mps": mps,
				},
			}
			s.ww.SendBroadcast(sp)
		}
	}
}

func (s *Controller) NewConnectionHandler(ac *ActiveClient) {
	sp, top := s.getQuestions("")
	ac.PushChan <- sp
	if top != "" {
		ac.Context["selected_question"] = top
		ans, _ := s.getAnswer(ac.Session, top)

		sp, top = s.getAnswers(top, ans)
		ac.PushChan <- sp

		if ans != "" {
			ac.PushChan <- s.getSelectedAnswer(ans)
		}
	}
}

func (s *Controller) HandleAnswerOld(request *Request, ac *ActiveClient) {
	falcore.Info("Got %#v", request)
	q, ok := request.Data["question"].(string)
	a, ok2 := request.Data["answer"].(string)
	if ok && ok2 {
		s.voteAnswer(ac.Session, q, a)
		// TODO broadcast only to conns listening to the current question
		sp, _ := s.getAnswers(q, a)
		s.ww.SendBroadcast(sp)
	}
}

func (s *Controller) HandleSelectedQuestion(request *Request, ac *ActiveClient) {
	falcore.Info("Got %#v", request)
	q, ok := request.Data["question"].(string)
	if ok {
		ac.Context["selected_question"] = q
		sp, _ := s.getQuestions(q)
		ans, _ := s.getAnswer(ac.Session, q)
		spa, _ := s.getAnswers(q, ans)
		ac.PushChan <- sp
		ac.PushChan <- spa
		// send selected
		ac.PushChan <- s.getSelectedAnswer(ans)
	}
}

func (s *Controller) HandleAnswer(request *Request, ac *ActiveClient) {
	falcore.Info("Got %#v", request)
	q, ok := request.Data["question"].(string)
	ans, ok2 := request.Data["answer"].(string)
	sp := &ServerPush{
		Method: "error",
		Data:   map[string]interface{}{"error": "Aww, crap"},
	}
	if ok && ok2 {
		_, err := s.voteAnswer(ac.Session, q, ans)
		if err == nil {
			sp, _ = s.getAnswers(q, ans)
		}
	}
	// send reply first
	ac.PushChan <- sp
	// send selected
	ac.PushChan <- s.getSelectedAnswer(ans)
	// send to everyone else
	s.ww.SendBroadcast(sp)

}

func (s *Controller) voteAnswer(session, question, answer string) (float64, error) {
	prev_ans, err := s.getAnswer(session, question)
	if err != nil {
		falcore.Error("Hget err for session answer: %v", err)
		return 0.0, err
	}
	if answer == prev_ans {
		return 0.0, nil
	}

	// TODO multi?
	if prev_ans != "" && answer != prev_ans {
		resp := s.client.ZIncrBy(fmt.Sprintf("answers/%v", question), -1.0, prev_ans)
		if resp.Err() != nil {
			falcore.Error("ZDecrement err: %v", resp.Err())
			return 0.0, resp.Err()
		}
	}
	resp := s.client.HSet(fmt.Sprintf("user_answers/%v", session), question, answer)
	if resp.Err() != nil {
		falcore.Error("Hset err: %v", resp.Err())
		return 0.0, resp.Err()
	}
	sr := s.client.ZIncrBy(fmt.Sprintf("answers/%v", question), 1.0, answer)
	return sr.Val(), sr.Err()
}

func (s *Controller) getAnswer(session, question string) (string, error) {
	ans := s.client.HGet(fmt.Sprintf("user_answers/%v", session), question)
	if ans.Err() != nil && ans.Err().Error() == "(nil)" {
		return "", nil
	}
	return ans.Val(), ans.Err()
}

func (s *Controller) getSelectedAnswer(answer string) *ServerPush {
	return &ServerPush{
		Method: "answer-selected",
		Data:   map[string]interface{}{"answer": &RAnswer{answer, 0.0, true, crc32.ChecksumIEEE([]byte(answer))}},
	}
}

func (s *Controller) getAnswers(question, selected string) (*ServerPush, string) {
	resp := s.client.ZRangeByScoreWithScoresMap(fmt.Sprintf("answers/%v", question), "-inf", "+inf", 0, 1000)
	if resp.Err() != nil {
		falcore.Error("Zrange err: %v", resp.Err())
		return &ServerPush{
			Method: "error",
			Data:   map[string]interface{}{"error": "Aww, crap"},
		}, ""
	}
	mp := resp.Val()
	falcore.Debug("as: %#v", mp)
	answers := make(ByScore, len(mp))
	i := 0
	for q, s := range mp {
		x := &RAnswer{q, s, false, crc32.ChecksumIEEE([]byte(q))}
		if selected == q {
			x.Selected = true
		}
		answers[i] = x
		i++
	}
	sort.Sort(answers)
	top := ""
	if len(answers) > 0 {
		top = answers[0].(*RAnswer).Answer
	}
	data := make(map[string]interface{})
	data["question"] = question
	data["answers"] = answers
	return &ServerPush{
		Method: "answers",
		Data:   data,
	}, top
}

func (s *Controller) getQuestions(selected string) (*ServerPush, string) {
	resp := s.client.ZRangeByScoreWithScoresMap("questions/current", "-inf", "+inf", 0, 1000)
	if resp.Err() != nil {
		falcore.Error("Zrange err: %v", resp.Err())
		return &ServerPush{
			Method: "error",
			Data:   map[string]interface{}{"error": "Aww, crap"},
		}, ""
	}
	mp := resp.Val()
	falcore.Debug("qs: %#v", mp)
	questions := make(ByScore, len(mp))
	i := 0
	for q, s := range mp {
		x := &RQuestion{q, s, false, crc32.ChecksumIEEE([]byte(q))}
		if selected == q {
			x.Selected = true
		}
		questions[i] = x
		i++
	}
	sort.Sort(questions)
	top := ""
	if len(questions) > 0 {
		topRQ := questions[0].(*RQuestion)
		top = topRQ.Question
		if selected == "" {
			topRQ.Selected = true
		}
	}
	data := make(map[string]interface{})
	data["questions"] = questions
	return &ServerPush{
		Method: "questions",
		Data:   data,
	}, top
}

func (s *Controller) voteQuestion(question string) (float64, error) {
	sr := s.client.ZIncrBy("questions/current", 1.0, question)
	return sr.Val(), sr.Err()
}

type Votable interface {
	Votes() float64
}
type RQuestion struct {
	Question string  `json:"question"`
	VotesF   float64 `json:"votes"`
	Selected bool    `json:"selected"`
	Id       uint32  `json:"id"`
}

func (rq *RQuestion) Votes() float64 { return rq.VotesF }

type RAnswer struct {
	Answer   string  `json:"answer"`
	VotesF   float64 `json:"votes"`
	Selected bool    `json:"selected"`
	Id       uint32  `json:"id"`
}

func (rq *RAnswer) Votes() float64 { return rq.VotesF }

type ByScore []Votable

func (a ByScore) Len() int           { return len(a) }
func (a ByScore) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByScore) Less(i, j int) bool { return a[j].Votes() < a[i].Votes() }
